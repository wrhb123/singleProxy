package server

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"singleproxy/pkg/config"
	"singleproxy/pkg/logger"
	"singleproxy/pkg/utils"

	"github.com/gorilla/websocket"
	"github.com/h12w/go-socks5"
	"golang.org/x/time/rate"
)

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
	config         *config.Config
	nextRequestID  uint64

	// 每个 key 的速率限制器
	keyLimiters map[string]*rate.Limiter
	// 每个 IP 的速率限制器
	ipLimiters map[string]*rate.Limiter
	// 保护 rate limiters map 的互斥锁
	rateLimitMu sync.RWMutex

	// SOCKS5 服务器
	socksServer *socks5.Server
}

// NewSinglePortProxy 创建一个新的服务器实例
func NewSinglePortProxy(cfg *config.Config) *SinglePortProxy {
	// 创建SOCKS5服务器配置
	socksConf := &socks5.Config{
		// 不需要认证
		AuthMethods: []socks5.Authenticator{
			&socks5.NoAuthAuthenticator{},
		},
	}
	socksServer, _ := socks5.New(socksConf)

	return &SinglePortProxy{
		clientConns:    make(map[string]*websocket.Conn),
		streamHandlers: make(map[uint64]*streamHandler),
		config:         cfg,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		keyLimiters: make(map[string]*rate.Limiter),
		ipLimiters:  make(map[string]*rate.Limiter),
		socksServer: socksServer,
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
		if err != nil {
			return fmt.Errorf("failed to listen on port %s: %v", p.config.ListenPort, err)
		}
		logger.Info("Server listening with TLS on port %s", p.config.ListenPort)
	} else {
		listener, err = net.Listen("tcp", ":"+p.config.ListenPort)
		if err != nil {
			return fmt.Errorf("failed to listen on port %s: %v", p.config.ListenPort, err)
		}
		logger.Info("Server listening without TLS on port %s", p.config.ListenPort)
	}

	logger.Info("Server supports: HTTP/WebSocket tunneling and SOCKS5 proxy")

	for {
		conn, err := listener.Accept()
		if err != nil {
			logger.Error("Failed to accept connection: %v", err)
			continue
		}

		// 为每个连接启动一个协程处理协议检测
		go p.handleConnection(conn)
	}
}

// handleConnection 检测连接协议类型并分发处理
func (p *SinglePortProxy) handleConnection(conn net.Conn) {
	remoteAddr := conn.RemoteAddr().String()
	logger.Debug("New connection received",
		"remote_addr", remoteAddr,
		"local_addr", conn.LocalAddr().String())

	// 读取前几个字节来判断协议类型
	buf := make([]byte, 16) // 增加缓冲区大小以更好地识别协议
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		logger.Error("Failed to set read deadline",
			"remote_addr", remoteAddr,
			"error", err)
		conn.Close()
		return
	}

	n, err := conn.Read(buf)
	if err != nil {
		logger.Error("Failed to read protocol bytes",
			"remote_addr", remoteAddr,
			"error", err)
		conn.Close()
		return
	}

	// 清除读取超时
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		logger.Error("Failed to clear read deadline",
			"remote_addr", remoteAddr,
			"error", err)
		conn.Close()
		return
	}

	// 使用实际读取的数据
	actualBuf := buf[:n]

	// 记录协议检测的详细信息
	logger.Debug("Protocol detection",
		"remote_addr", remoteAddr,
		"bytes_read", n,
		"first_byte", fmt.Sprintf("0x%02x", actualBuf[0]),
		"data_preview", fmt.Sprintf("%q", string(actualBuf[:utils.Min(n, 10)])))

	// SOCKS5协议的第一个字节是版本号0x05
	if len(actualBuf) > 0 && actualBuf[0] == 0x05 {
		logger.Info("Detected SOCKS5 protocol",
			"remote_addr", remoteAddr,
			"version", fmt.Sprintf("0x%02x", actualBuf[0]))

		// 创建一个可以回放所有字节的连接包装器
		wrappedConn := &prefixedConn{
			Conn:   conn,
			prefix: actualBuf,
		}

		// SOCKS5处理，连接由SOCKS5库管理
		startTime := time.Now()
		if err := p.socksServer.ServeConn(wrappedConn); err != nil {
			duration := time.Since(startTime)
			// 区分不同类型的SOCKS5错误，提供更友好的日志
			errMsg := err.Error()
			if strings.Contains(errMsg, "connection reset by peer") {
				logger.Warn("SOCKS5 client disconnected unexpectedly",
					"remote_addr", remoteAddr,
					"duration", duration,
					"reason", "network_issue")
			} else if strings.Contains(errMsg, "i/o timeout") {
				logger.Warn("SOCKS5 connection timed out",
					"remote_addr", remoteAddr,
					"duration", duration,
					"reason", "timeout")
			} else if strings.Contains(errMsg, "EOF") {
				logger.Debug("SOCKS5 client closed connection normally",
					"remote_addr", remoteAddr,
					"duration", duration)
			} else {
				logger.Error("SOCKS5 connection error",
					"remote_addr", remoteAddr,
					"duration", duration,
					"error", err)
			}
		} else {
			duration := time.Since(startTime)
			logger.Info("SOCKS5 session completed successfully",
				"remote_addr", remoteAddr,
				"duration", duration)
		}
	} else {
		// HTTP协议 - 直接处理这个连接而不是包装成listener
		logger.Info("Detected HTTP protocol",
			"remote_addr", remoteAddr,
			"data_preview", fmt.Sprintf("%q", string(actualBuf[:utils.Min(n, 10)])))

		wrappedConn := &prefixedConn{
			Conn:   conn,
			prefix: actualBuf,
		}

		// 直接处理HTTP连接，而不是通过HTTP服务器
		p.handleHTTPConnection(wrappedConn)
	}
}

// handleHTTPConnection 直接处理HTTP连接（包括WebSocket升级）
func (p *SinglePortProxy) handleHTTPConnection(conn net.Conn) {
	remoteAddr := conn.RemoteAddr().String()

	logger.Debug("Handling HTTP connection",
		"remote_addr", remoteAddr,
		"local_addr", conn.LocalAddr().String())

	// 读取HTTP请求
	reader := bufio.NewReader(conn)
	req, err := http.ReadRequest(reader)
	if err != nil {
		logger.Error("Failed to read HTTP request",
			"remote_addr", remoteAddr,
			"error", err)
		conn.Close()
		return
	}

	logger.Debug("Successfully read HTTP request",
		"remote_addr", remoteAddr,
		"method", req.Method,
		"url", req.URL.String(),
		"proto", req.Proto,
		"host", req.Host,
		"user_agent", req.Header.Get("User-Agent"),
		"content_length", req.ContentLength)

	// 设置正确的RemoteAddr，这对于速率限制很重要
	if req.RemoteAddr == "" {
		req.RemoteAddr = conn.RemoteAddr().String()
		logger.Debug("Set request RemoteAddr",
			"remote_addr", remoteAddr)
	}

	// 创建响应写入器
	w := &httpResponseWriter{
		conn:   conn,
		header: make(http.Header),
	}

	logger.Debug("Created HTTP response writer",
		"remote_addr", remoteAddr)

	// 调用我们的HTTP处理器
	startTime := time.Now()
	p.ServeHTTP(w, req)
	duration := time.Since(startTime)

	logger.Debug("HTTP request processing completed",
		"remote_addr", remoteAddr,
		"method", req.Method,
		"url", req.URL.String(),
		"duration", duration,
		"hijacked", w.hijacked)

	// 如果不是WebSocket连接且没有被hijack，关闭连接
	if !w.hijacked {
		logger.Debug("Closing HTTP connection",
			"remote_addr", remoteAddr,
			"reason", "not_hijacked")
		conn.Close()
	}
}

// ServeHTTP 是 http.Handler 接口的实现，用于路由请求
func (p *SinglePortProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 记录所有HTTP请求的debug信息
	logger.Debug("Received HTTP request",
		"method", r.Method,
		"url", r.URL.String(),
		"remote_addr", r.RemoteAddr,
		"user_agent", r.Header.Get("User-Agent"),
		"content_length", r.ContentLength,
		"headers", utils.SanitizeHeaders(r.Header))

	// 路由1: 处理来自内网客户端的 WebSocket 隧道连接
	if strings.HasPrefix(r.URL.Path, "/ws/") {
		logger.Debug("Routing to tunnel registration handler",
			"path", r.URL.Path,
			"remote_addr", r.RemoteAddr)
		p.handleTunnelRegistration(w, r)
		return
	}

	// 路由2: 处理来自公网的普通 HTTP 请求 (内网穿透)
	logger.Debug("Routing to public HTTP request handler",
		"path", r.URL.Path,
		"remote_addr", r.RemoteAddr)
	p.handlePublicHTTPRequest(w, r)
}

// handleTunnelRegistration 处理内网客户端的隧道注册请求
func (p *SinglePortProxy) handleTunnelRegistration(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/ws/")
	remoteAddr := r.RemoteAddr

	logger.Debug("Processing tunnel registration request",
		"key", key,
		"remote_addr", remoteAddr,
		"user_agent", r.Header.Get("User-Agent"),
		"headers", utils.SanitizeHeaders(r.Header))

	if key == "" {
		logger.Warn("Tunnel registration failed - empty key",
			"remote_addr", remoteAddr,
			"path", r.URL.Path)
		http.Error(w, "Tunnel key cannot be empty", http.StatusBadRequest)
		return
	}

	logger.Info("Attempting to upgrade connection to WebSocket",
		"key", key,
		"remote_addr", remoteAddr)

	wsConn, err := p.upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Error("Failed to upgrade connection to WebSocket",
			"key", key,
			"remote_addr", remoteAddr,
			"error", err)
		return
	}

	logger.Info("Tunnel client connected successfully",
		"key", key,
		"remote_addr", wsConn.RemoteAddr())

	p.connsMu.Lock()
	if oldConn, ok := p.clientConns[key]; ok {
		logger.Info("Replacing existing connection for key",
			"key", key,
			"old_remote_addr", oldConn.RemoteAddr(),
			"new_remote_addr", wsConn.RemoteAddr())
		oldConn.Close()

		// 清理与该连接相关的待处理请求，避免请求ID冲突
		p.handlersMu.Lock()
		cleanupCount := 0
		for reqID, handler := range p.streamHandlers {
			// 简单的启发式方法：如果handler已经等待很久，可能是断线前的请求
			select {
			case <-handler.done:
				// 已完成，跳过
			default:
				// 未完成，清理它
				close(handler.done)
				delete(p.streamHandlers, reqID)
				cleanupCount++
			}
		}
		p.handlersMu.Unlock()

		if cleanupCount > 0 {
			logger.Info("Cleaned up pending requests for reconnected key",
				"key", key,
				"cleanup_count", cleanupCount)
		}
	}
	p.clientConns[key] = wsConn

	// 记录当前活跃连接数
	connectionCount := len(p.clientConns)
	p.connsMu.Unlock()

	logger.Info("Tunnel registered successfully",
		"key", key,
		"remote_addr", wsConn.RemoteAddr(),
		"total_active_tunnels", connectionCount)

	p.clientReadLoop(wsConn, key)
}
