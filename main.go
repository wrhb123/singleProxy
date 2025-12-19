package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/time/rate"
)

const (
	// WebSocket 消息类型
	MSG_TYPE_HTTP_REQ       = 1
	MSG_TYPE_HTTP_RES       = 2
	MSG_TYPE_HTTP_RES_CHUNK = 3 // 新增：用于传输响应体数据块
)

// TunnelMessage 定义了隧道中传输的消息格式
type TunnelMessage struct {
	ID      uint64
	Type    uint8
	Payload []byte
}

// Config 结构体用于存储命令行参数
type Config struct {
	Mode       string // "server" or "client"
	ListenPort string // Server listening port
	ServerAddr string // Server address for client to connect to (e.g., wss://example.com:443)
	TargetAddr string // Target service address for client to forward to (e.g., 127.0.0.1:8080)
	Key        string // Tunnel key for identifying the service
	CertFile   string // TLS cert file for server
	KeyFile    string // TLS key file for server
	Insecure   bool   // Skip TLS certificate verification for client

	IPRateLimit  int // 每个IP每秒的请求限制
	KeyRateLimit int // 每个key每秒的请求限制
}

// ==================== Server 实现 ====================

// streamHandler 用于处理一个流式响应
type streamHandler struct {
	writer  http.ResponseWriter
	flusher http.Flusher
	done    chan struct{}
}

// SinglePortProxy 是服务器端组件
type SinglePortProxy struct {
	clientConns    map[string]*websocket.Conn
	connsMu        sync.RWMutex
	streamHandlers map[uint64]*streamHandler
	handlersMu     sync.Mutex
	upgrader       websocket.Upgrader
	config         *Config
	nextRequestID  uint64

	// 每个 key 的速率限制器
	keyLimiters map[string]*rate.Limiter
	// 每个 IP 的速率限制器
	ipLimiters map[string]*rate.Limiter
	// 保护 rate limiters map 的互斥锁
	rateLimitMu sync.RWMutex
}

// NewSinglePortProxy 创建一个新的服务器实例
func NewSinglePortProxy(config *Config) *SinglePortProxy {
	return &SinglePortProxy{
		clientConns:    make(map[string]*websocket.Conn),
		streamHandlers: make(map[uint64]*streamHandler),
		config:         config,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		keyLimiters: make(map[string]*rate.Limiter),
		ipLimiters:  make(map[string]*rate.Limiter),
	}
}

// Start 启动服务器
func (p *SinglePortProxy) Start() error {
	var listener net.Listener
	var err error

	if p.config.CertFile != "" && p.config.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(p.config.CertFile, p.config.KeyFile)
		if err != nil {
			return fmt.Errorf("failed to load TLS certificate: %v", err)
		}
		tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}}
		listener, err = tls.Listen("tcp", ":"+p.config.ListenPort, tlsConfig)
		log.Printf("Server listening with TLS on port %s", p.config.ListenPort)
	} else {
		listener, err = net.Listen("tcp", ":"+p.config.ListenPort)
		log.Printf("Server listening without TLS on port %s", p.config.ListenPort)
	}

	if err != nil {
		return fmt.Errorf("failed to listen on port %s: %v", p.config.ListenPort, err)
	}

	server := &http.Server{Handler: p}
	return server.Serve(listener)
}

// ServeHTTP 是 http.Handler 接口的实现，用于路由请求
func (p *SinglePortProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 路由1: 处理来自内网客户端的 WebSocket 隧道连接
	if strings.HasPrefix(r.URL.Path, "/ws/") {
		p.handleTunnelRegistration(w, r)
		return
	}

	// 路由2: 处理来自公网的代理请求 (正向代理)
	if r.Header.Get("X-Proxy-Key") != "" {
		p.handleForwardProxyRequest(w, r)
		return
	}

	// 路由3: 处理来自公网的普通 HTTP 请求 (内网穿透)
	p.handlePublicHTTPRequest(w, r)
}

// handleForwardProxyRequest 处理标准的HTTP/HTTPS正向代理请求
func (p *SinglePortProxy) handleForwardProxyRequest(w http.ResponseWriter, r *http.Request) {
	log.Printf("PROXY Request: %s %s", r.Method, r.URL.String())

	// 对于HTTPS的CONNECT方法，需要特殊处理，这里先实现HTTP的GET/POST等
	if r.Method == "CONNECT" {
		http.Error(w, "CONNECT method is not yet supported in this proxy mode", http.StatusNotImplemented)
		return
	}

	// 创建一个用于转发到目标服务器的 http.Client
	// 设置 Proxy 函数为 nil，可以防止代理请求被再次转发，形成代理循环
	transport := &http.Transport{
		Proxy: func(req *http.Request) (*url.URL, error) {
			return nil, nil // 表示直连，不使用任何代理
		},
	}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}

	// 克隆请求，因为原始请求的 Body 只能读取一次
	outReq := r.Clone(r.Context())

	// 必须清空 RequestURI，否则 client.Do 会报错
	outReq.RequestURI = ""

	// 发起请求到目标服务器
	resp, err := client.Do(outReq)
	if err != nil {
		log.Printf("Proxy error when connecting to target %s: %v", r.URL.Host, err)
		http.Error(w, "Proxy Error: Failed to connect to target", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// 将目标服务器的响应头复制回给客户端
	for key, values := range resp.Header {
		w.Header()[key] = values
	}

	// 将目标服务器的状态码复制回给客户端
	w.WriteHeader(resp.StatusCode)

	// 将目标服务器的响应体流式复制回给客户端
	// io.Copy 会高效地处理数据传输，无需将整个响应体读入内存
	io.Copy(w, resp.Body)
}

// handleTunnelRegistration 处理内网客户端的隧道注册请求
func (p *SinglePortProxy) handleTunnelRegistration(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/ws/")
	if key == "" {
		http.Error(w, "Tunnel key cannot be empty", http.StatusBadRequest)
		return
	}

	wsConn, err := p.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Failed to upgrade connection for key '%s': %v", key, err)
		return
	}
	log.Printf("Tunnel client connected for key: %s from %s", key, wsConn.RemoteAddr())

	p.connsMu.Lock()
	if oldConn, ok := p.clientConns[key]; ok {
		oldConn.Close()
	}
	p.clientConns[key] = wsConn
	p.connsMu.Unlock()

	p.clientReadLoop(wsConn, key)
}

// clientReadLoop 是唯一的读取器，处理来自客户端的所有消息 (支持流式传输)
func (p *SinglePortProxy) clientReadLoop(wsConn *websocket.Conn, key string) {
	defer func() {
		wsConn.Close()
		p.connsMu.Lock()
		delete(p.clientConns, key)
		p.connsMu.Unlock()
		log.Printf("Tunnel client for key '%s' disconnected", key)
	}()

	wsConn.SetReadLimit(10 * 1024 * 1024)
	wsConn.SetReadDeadline(time.Now().Add(60 * time.Second))
	wsConn.SetPongHandler(func(string) error {
		wsConn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, data, err := wsConn.ReadMessage()
		if err != nil {
			log.Printf("Error reading from WebSocket for key '%s': %v", key, err)
			break
		}

		msg, err := deserializeTunnelMessage(data)
		if err != nil {
			log.Printf("Failed to deserialize tunnel message from key '%s': %v", key, err)
			continue
		}

		p.handlersMu.Lock()
		handler, ok := p.streamHandlers[msg.ID]
		if !ok {
			// 如果找不到处理器，说明这是一个新的请求
			if msg.Type == MSG_TYPE_HTTP_RES {
				log.Printf("Received response for unknown request ID: %d", msg.ID)
			}
			p.handlersMu.Unlock()
			continue
		}

		if msg.Type == MSG_TYPE_HTTP_RES {
			// 收到响应头
			resp, err := deserializeHTTPResponse(msg.Payload)
			if err != nil {
				log.Printf("Failed to deserialize response header for ID %d: %v", msg.ID, err)
				delete(p.streamHandlers, msg.ID)
				close(handler.done)
				p.handlersMu.Unlock()
				continue
			}

			// 将响应头写回给公网用户
			for k, v := range resp.Header {
				handler.writer.Header()[k] = v
			}
			handler.writer.WriteHeader(resp.StatusCode)
			handler.flusher.Flush() // 立即发送头部

		} else if msg.Type == MSG_TYPE_HTTP_RES_CHUNK {
			// 收到响应体数据块
			if len(msg.Payload) > 0 {
				if _, err := handler.writer.Write(msg.Payload); err != nil {
					log.Printf("Failed to write chunk to response for ID %d: %v", msg.ID, err)
				}
				handler.flusher.Flush() // 立即发送数据块
			} else {
				// 收到空的数据块，表示流结束
				close(handler.done)
				delete(p.streamHandlers, msg.ID)
			}
		}
		p.handlersMu.Unlock()
	}
}

// getLimiter 获取或创建一个指定 key 的速率限制器
func (p *SinglePortProxy) getKeyLimiter(key string) *rate.Limiter {
	p.rateLimitMu.Lock()
	defer p.rateLimitMu.Unlock()

	limiter, exists := p.keyLimiters[key]
	if !exists {
		// 如果配置为0，则不进行限制
		if p.config.KeyRateLimit <= 0 {
			// 返回一个总是允许的限制器
			limiter = rate.NewLimiter(rate.Inf, 0)
		} else {
			// 创建一个新的限制器: 每秒 N 个请求，突发 2N 个
			limiter = rate.NewLimiter(rate.Limit(p.config.KeyRateLimit), p.config.KeyRateLimit*2)
		}
		p.keyLimiters[key] = limiter
	}

	return limiter
}

// getIPLimiter 获取或创建一个指定 IP 的速率限制器
func (p *SinglePortProxy) getIPLimiter(ip string) *rate.Limiter {
	p.rateLimitMu.Lock()
	defer p.rateLimitMu.Unlock()

	limiter, exists := p.ipLimiters[ip]
	if !exists {
		// 如果配置为0，则不进行限制
		if p.config.IPRateLimit <= 0 {
			// 返回一个总是允许的限制器
			limiter = rate.NewLimiter(rate.Inf, 0)
		} else {
			// 创建一个新的限制器: 每秒 N 个请求，突发 2N 个
			limiter = rate.NewLimiter(rate.Limit(p.config.IPRateLimit), p.config.IPRateLimit*2)
		}
		p.ipLimiters[ip] = limiter
	}

	return limiter
}

// handlePublicHTTPRequest 处理来自公网的请求 (支持流式传输) 增加速率限制
func (p *SinglePortProxy) handlePublicHTTPRequest(w http.ResponseWriter, r *http.Request) {
	// 检查 IP 速率限制
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	ipLimiter := p.getIPLimiter(ip)
	if !ipLimiter.Allow() {
		log.Printf("IP %s rate limited", ip)
		http.Error(w, "Too many requests from your IP", http.StatusTooManyRequests)
		return
	}

	// 2. 获取密钥
	key := r.Header.Get("X-Tunnel-Key")
	if key == "" {
		key = "default"
	}

	// 检查 Key 速率限制
	keyLimiter := p.getKeyLimiter(key)
	if !keyLimiter.Allow() {
		log.Printf("Key '%s' rate limited", key)
		http.Error(w, "Too many requests for this service", http.StatusTooManyRequests)
		return
	}

	p.connsMu.RLock()
	wsConn, ok := p.clientConns[key]
	p.connsMu.RUnlock()

	if !ok {
		log.Printf("No active tunnel for key: %s", key)
		http.Error(w, "Service unavailable", http.StatusBadGateway)
		return
	}

	reqData, err := serializeHTTPRequest(r)
	if err != nil {
		log.Printf("Failed to serialize request for key '%s': %v", key, err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	requestID := atomic.AddUint64(&p.nextRequestID, 1)

	// 检查 ResponseWriter 是否支持 Flusher
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	done := make(chan struct{})
	handler := &streamHandler{
		writer:  w,
		flusher: flusher,
		done:    done,
	}

	p.handlersMu.Lock()
	p.streamHandlers[requestID] = handler
	p.handlersMu.Unlock()

	tunnelMsg := TunnelMessage{ID: requestID, Type: MSG_TYPE_HTTP_REQ, Payload: reqData}
	msgData, _ := serializeTunnelMessage(tunnelMsg)

	if err := wsConn.WriteMessage(websocket.BinaryMessage, msgData); err != nil {
		log.Printf("Failed to send request to client for key '%s': %v", key, err)
		p.handlersMu.Lock()
		delete(p.streamHandlers, requestID)
		p.handlersMu.Unlock()
		http.Error(w, "Failed to forward request", http.StatusBadGateway)
		return
	}

	// 等待流结束或超时
	select {
	case <-handler.done:
		// 流正常结束
	case <-time.After(60 * time.Second):
		log.Printf("Timeout waiting for response stream for key '%s'", key)
		p.handlersMu.Lock()
		delete(p.streamHandlers, requestID)
		p.handlersMu.Unlock()
		http.Error(w, "Gateway Timeout", http.StatusGatewayTimeout)
	}
}

// ==================== Client 实现 ====================

// TunnelClient 是客户端组件
type TunnelClient struct {
	serverAddr *url.URL
	targetAddr string
	key        string
	wsConn     *websocket.Conn
	tlsConfig  *tls.Config
	writeChan  chan []byte
	closeChan  chan struct{}
}

// NewTunnelClient 创建一个新的客户端实例
func NewTunnelClient(config *Config) (*TunnelClient, error) {
	serverURL, err := url.Parse(config.ServerAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid server address: %v", err)
	}
	if serverURL.Scheme != "ws" && serverURL.Scheme != "wss" {
		return nil, fmt.Errorf("server address scheme must be 'ws' or 'wss'")
	}

	tlsConfig := &tls.Config{InsecureSkipVerify: config.Insecure}

	return &TunnelClient{
		serverAddr: serverURL,
		targetAddr: config.TargetAddr,
		key:        config.Key,
		tlsConfig:  tlsConfig,
		writeChan:  make(chan []byte, 256),
		// closeChan 已从此处移除
	}, nil
}

// writer 是唯一的写入器，通过 channel 接收所有待发送的数据
func (c *TunnelClient) writer() {
	defer c.wsConn.Close()

	for {
		select {
		case message := <-c.writeChan:
			if err := c.wsConn.WriteMessage(websocket.BinaryMessage, message); err != nil {
				log.Printf("Error writing to WebSocket: %v", err)
				return
			}
		case <-c.closeChan:
			return
		}
	}
}

// readLoop 是唯一的读取器，处理来自服务器的所有消息 (修改版)
func (c *TunnelClient) readLoop() {
	defer close(c.closeChan) // 通知 writer 和 keepAlive 退出

	c.wsConn.SetReadLimit(10 * 1024 * 1024)
	c.wsConn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.wsConn.SetPongHandler(func(string) error {
		c.wsConn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, data, err := c.wsConn.ReadMessage()
		if err != nil {
			log.Printf("Connection error: %v", err)
			break
		}

		msg, err := deserializeTunnelMessage(data)
		if err != nil {
			log.Printf("Failed to deserialize tunnel message: %v", err)
			continue
		}

		if msg.Type == MSG_TYPE_HTTP_REQ {
			// 将完整的消息（包含ID）传递给处理函数
			go c.handleHTTPRequest(msg)
		}
	}
}

// handleHTTPRequest 处理单个HTTP请求 (流式传输版 - 修复竞态条件)
func (c *TunnelClient) handleHTTPRequest(reqMsg TunnelMessage) {
	log.Println("DEBUG: handleHTTPRequest started.")
	req, err := parseHTTPRequest(reqMsg.Payload)
	if err != nil {
		log.Printf("Failed to parse HTTP request: %v", err)
		return
	}
	log.Printf("DEBUG: Parsed request for %s %s", req.Method, req.URL.String())

	resp, err := forwardToTarget(req, c.targetAddr)
	if err != nil {
		log.Printf("Failed to forward request to %s: %v", c.targetAddr, err)
		return
	}
	// 错误的 defer 语句已从此处移除！
	// defer resp.Body.Close()
	log.Printf("DEBUG: forwardToTarget succeeded. Status: %s", resp.Status)

	// 1. 先发送响应头
	headerBuf := new(bytes.Buffer)
	fmt.Fprintf(headerBuf, "HTTP/1.1 %s\r\n", resp.Status)
	resp.Header.Write(headerBuf)
	headerBuf.WriteString("\r\n")

	headerMsg := TunnelMessage{ID: reqMsg.ID, Type: MSG_TYPE_HTTP_RES, Payload: headerBuf.Bytes()}
	headerData, _ := serializeTunnelMessage(headerMsg)

	select {
	case c.writeChan <- headerData:
		log.Println("DEBUG: Response header successfully queued for writing.")
	case <-time.After(10 * time.Second):
		log.Println("DEBUG: FAILED to queue response header for writing.")
		return // 如果头都发不出去，后面的也没意义了
	}

	// 2. 流式发送响应体
	// streamResponseBody 函数内部会负责关闭 resp.Body
	go c.streamResponseBody(resp.Body, reqMsg.ID)
}

// streamResponseBody 流式地读取响应体并发送数据块
func (c *TunnelClient) streamResponseBody(body io.ReadCloser, requestID uint64) {
	defer body.Close()
	buf := make([]byte, 32*1024) // 32KB 的缓冲区

	for {
		n, err := body.Read(buf)
		if n > 0 {
			chunkMsg := TunnelMessage{ID: requestID, Type: MSG_TYPE_HTTP_RES_CHUNK, Payload: buf[:n]}
			chunkData, _ := serializeTunnelMessage(chunkMsg)

			select {
			case c.writeChan <- chunkData:
				// 数据块已排队
			case <-c.closeChan:
				// 连接已关闭，退出
				log.Println("DEBUG: Connection closed while streaming body.")
				return
			}
		}

		if err != nil {
			if err != io.EOF {
				log.Printf("Error while reading response body: %v", err)
			}
			break // 读取完毕或出错，退出循环
		}
	}
	log.Println("DEBUG: Response body streaming finished.")
}

func (c *TunnelClient) keepAlive() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// 使用 WriteControl 来发送 Ping，它是线程安全的，不会与 writer goroutine 冲突
			if err := c.wsConn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				log.Printf("Keep-alive failed: %v", err)
				return
			}
		case <-c.closeChan:
			return
		}
	}
}

// Connect 连接到服务器并建立隧道 (修改为非阻塞)
func (c *TunnelClient) Connect() error {
	// 在建立新连接前，确保旧的连接已关闭
	if c.wsConn != nil {
		c.wsConn.Close()
	}

	connURL := *c.serverAddr
	connURL.Path = "/ws/" + c.key

	dialer := websocket.DefaultDialer
	dialer.TLSClientConfig = c.tlsConfig

	wsConn, _, err := dialer.Dial(connURL.String(), nil)
	if err != nil {
		return fmt.Errorf("failed to connect to server: %v", err)
	}
	c.wsConn = wsConn
	log.Printf("Connected to server at %s, tunnel for key '%s' established", c.serverAddr.String(), c.key)

	// 启动后台goroutines
	go c.readLoop()
	go c.writer()
	go c.keepAlive()

	return nil
}

// Run 启动客户端并保持运行，支持自动重连 (修复版)
func (c *TunnelClient) Run() {
	for {
		// 在每次尝试连接前，都创建一个新的 closeChan
		c.closeChan = make(chan struct{})
		log.Println("Attempting to connect to the server...")
		err := c.Connect()
		if err != nil {
			log.Printf("Connection failed: %v. Retrying in 10 seconds...", err)
			time.Sleep(10 * time.Second)
			continue
		}

		log.Println("Client is running. Waiting for disconnection...")
		// 阻塞，直到连接断开
		<-c.closeChan
		log.Println("Connection lost. Reconnecting in 10 seconds...")
		time.Sleep(10 * time.Second)
	}
}

// ==================== 共享工具函数 ====================

func serializeTunnelMessage(msg TunnelMessage) ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := binary.Write(buf, binary.BigEndian, msg.ID); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.BigEndian, msg.Type); err != nil {
		return nil, err
	}
	if _, err := buf.Write(msg.Payload); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func deserializeTunnelMessage(data []byte) (TunnelMessage, error) {
	if len(data) < 9 { // 8 bytes ID + 1 byte Type
		return TunnelMessage{}, errors.New("message too short")
	}
	msg := TunnelMessage{
		ID:   binary.BigEndian.Uint64(data[:8]),
		Type: data[8],
	}
	msg.Payload = data[9:]
	return msg, nil
}

func serializeHTTPRequest(r *http.Request) ([]byte, error) {
	var buf bytes.Buffer
	// 重建请求行
	reqURL := *r.URL
	reqURL.Scheme = "http"
	reqURL.Host = r.Host
	fmt.Fprintf(&buf, "%s %s HTTP/1.1\r\n", r.Method, reqURL.RequestURI())
	r.Header.Write(&buf)
	buf.WriteString("\r\n")
	if r.Body != nil {
		_, err := io.Copy(&buf, r.Body)
		if err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func parseHTTPRequest(data []byte) (*http.Request, error) {
	reader := bufio.NewReader(bytes.NewReader(data))
	req, err := http.ReadRequest(reader)
	if err != nil {
		return nil, err
	}
	return req, nil
}

func serializeHTTPResponse(resp *http.Response) []byte {
	var buf bytes.Buffer

	// 写入状态行，使用 resp.Status 字段，它本身就是 "200 OK" 这样的完整字符串
	fmt.Fprintf(&buf, "HTTP/1.1 %s\r\n", resp.Status)

	// 写入 Header
	resp.Header.Write(&buf)
	buf.WriteString("\r\n")

	// 写入 Body
	if resp.Body != nil {
		io.Copy(&buf, resp.Body)
	}
	return buf.Bytes()
}

func deserializeHTTPResponse(data []byte) (*http.Response, error) {
	reader := bufio.NewReader(bytes.NewReader(data))
	return http.ReadResponse(reader, nil)
}

func forwardToTarget(req *http.Request, targetAddr string) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = targetAddr
	req.RequestURI = ""
	req.Header.Del("Connection")
	req.Header.Del("Keep-Alive")
	req.Header.Del("Proxy-Authenticate")
	req.Header.Del("Proxy-Authorization")
	req.Header.Del("TE")
	req.Header.Del("Trailers")
	req.Header.Del("Transfer-Encoding")
	req.Header.Del("Upgrade")

	client := &http.Client{Timeout: 30 * time.Second}
	return client.Do(req)
}

// ==================== 主函数 ====================

func parseFlags() *Config {
	config := &Config{}
	flag.StringVar(&config.Mode, "mode", "server", "运行模式: server 或 client")
	flag.StringVar(&config.ListenPort, "port", "443", "服务器监听端口")
	flag.StringVar(&config.ServerAddr, "server", "", "服务器地址, e.g. wss://yourdomain.com (client模式)")
	flag.StringVar(&config.TargetAddr, "target", "", "目标服务地址, e.g. 127.0.0.1:8080 (client模式)")
	flag.StringVar(&config.Key, "key", "default", "隧道密钥")
	flag.StringVar(&config.CertFile, "cert", "", "TLS证书文件路径 (server模式)")
	flag.StringVar(&config.KeyFile, "key-file", "", "TLS私钥文件路径 (server模式)")
	flag.BoolVar(&config.Insecure, "insecure", false, "跳过TLS证书验证 (client模式)")
	flag.IntVar(&config.IPRateLimit, "ip-rate-limit", config.IPRateLimit, "每个IP每秒的请求限制 (0为无限制)")
	flag.IntVar(&config.KeyRateLimit, "key-rate-limit", config.KeyRateLimit, "每个key每秒的请求限制 (0为无限制)")

	flag.Parse()

	if config.Mode != "server" && config.Mode != "client" {
		log.Fatal("错误: 模式必须是 'server' 或 'client'")
	}
	if config.Mode == "client" {
		if config.ServerAddr == "" || config.TargetAddr == "" {
			log.Fatal("错误: client模式需要指定 -server 和 -target 参数")
		}
	}
	return config
}

func main() {
	config := parseFlags()

	if config.Mode == "server" {
		server := NewSinglePortProxy(config)
		log.Fatalf("服务器启动失败: %v", server.Start())
	} else {
		client, err := NewTunnelClient(config)
		if err != nil {
			log.Fatalf("创建客户端失败: %v", err)
		}
		client.Run()
	}
}
