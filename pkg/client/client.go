package client

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net/url"
	"time"

	"singleproxy/pkg/config"
	"singleproxy/pkg/logger"
	"singleproxy/pkg/protocol"
	"singleproxy/pkg/utils"

	"github.com/gorilla/websocket"
)

// TunnelClient 是客户端组件
type TunnelClient struct {
	serverAddr *url.URL
	targetAddr string
	key        string
	wsConn     *websocket.Conn
	tlsConfig  *tls.Config
	writeChan  chan []byte
	closeChan  chan struct{}

	// 连接健康状态监控
	lastPingTime   time.Time
	lastPongTime   time.Time
	reconnectCount int
}

// NewTunnelClient 创建一个新的客户端实例
func NewTunnelClient(config *config.Config) (*TunnelClient, error) {
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
		// closeChan 将在连接时创建
	}, nil
}

// writer 是唯一的写入器，通过 channel 接收所有待发送的数据
func (c *TunnelClient) writer() {
	defer c.wsConn.Close()

	for {
		select {
		case message := <-c.writeChan:
			if err := c.wsConn.WriteMessage(websocket.BinaryMessage, message); err != nil {
				logger.Error("Error writing to WebSocket",
					"key", c.key,
					"error", err)
				return
			}
		case <-c.closeChan:
			return
		}
	}
}

// readLoop 是唯一的读取器，处理来自服务器的所有消息 (修改版)
func (c *TunnelClient) readLoop() {
	logger.Info("Starting client read loop",
		"key", c.key,
		"server_addr", c.serverAddr.String(),
		"target_addr", c.targetAddr)

	defer func() {
		logger.Info("Exiting client read loop",
			"key", c.key)
		close(c.closeChan) // 通知 writer 和 keepAlive 退出
	}()

	c.wsConn.SetReadLimit(10 * 1024 * 1024)
	// 增加读取超时时间，避免过早断开连接
	readTimeout := 90 * time.Second
	_ = c.wsConn.SetReadDeadline(time.Now().Add(readTimeout))

	logger.Debug("Set WebSocket read configuration",
		"key", c.key,
		"read_limit", "10MB",
		"read_timeout", readTimeout)

	c.wsConn.SetPongHandler(func(string) error {
		c.lastPongTime = time.Now()
		_ = c.wsConn.SetReadDeadline(time.Now().Add(readTimeout))
		logger.Debug("Received pong from server, connection healthy",
			"key", c.key,
			"last_pong_time", c.lastPongTime)
		return nil
	})

	messageCount := 0
	for {
		_, data, err := c.wsConn.ReadMessage()
		if err != nil {
			// 区分不同的错误类型提供更详细的日志
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				logger.Info("WebSocket connection closed normally",
					"key", c.key,
					"error", err,
					"messages_processed", messageCount)
			} else if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				logger.Error("WebSocket connection closed unexpectedly",
					"key", c.key,
					"error", err,
					"messages_processed", messageCount)
			} else {
				logger.Error("WebSocket read error",
					"key", c.key,
					"error", err,
					"messages_processed", messageCount)
			}
			break
		}

		messageCount++
		logger.Debug("Received message from server",
			"key", c.key,
			"message_size", len(data),
			"total_messages", messageCount)

		msg, err := protocol.DeserializeTunnelMessage(data)
		if err != nil {
			logger.Error("Failed to deserialize tunnel message",
				"key", c.key,
				"message_size", len(data),
				"error", err)
			continue
		}

		logger.Debug("Deserialized tunnel message",
			"key", c.key,
			"message_id", msg.ID,
			"message_type", msg.Type,
			"payload_size", len(msg.Payload))

		if msg.Type == protocol.MSG_TYPE_HTTP_REQ {
			logger.Debug("Processing HTTP request",
				"key", c.key,
				"request_id", msg.ID,
				"payload_size", len(msg.Payload))
			// 将完整的消息（包含ID）传递给处理函数
			go c.handleHTTPRequest(msg)
		}
	}
}

// handleHTTPRequest 处理单个HTTP请求 (流式传输版 - 修复竞态条件)
func (c *TunnelClient) handleHTTPRequest(reqMsg protocol.TunnelMessage) {
	startTime := time.Now()
	logger.Debug("Starting HTTP request processing",
		"key", c.key,
		"request_id", reqMsg.ID,
		"payload_size", len(reqMsg.Payload))

	req, err := protocol.ParseHTTPRequest(reqMsg.Payload)
	if err != nil {
		logger.Error("Failed to parse HTTP request",
			"key", c.key,
			"request_id", reqMsg.ID,
			"error", err)
		return
	}

	logger.Debug("Parsed HTTP request",
		"key", c.key,
		"request_id", reqMsg.ID,
		"method", req.Method,
		"url", req.URL.String(),
		"target_addr", c.targetAddr,
		"content_length", req.ContentLength,
		"headers", utils.SanitizeHeaders(req.Header))

	forwardStart := time.Now()
	resp, err := utils.ForwardToTarget(req, c.targetAddr)
	forwardDuration := time.Since(forwardStart)

	if err != nil {
		logger.Error("Failed to forward request to target",
			"key", c.key,
			"request_id", reqMsg.ID,
			"target_addr", c.targetAddr,
			"method", req.Method,
			"url", req.URL.String(),
			"duration", forwardDuration,
			"error", err)
		return
	}

	logger.Debug("Successfully forwarded request to target",
		"key", c.key,
		"request_id", reqMsg.ID,
		"target_addr", c.targetAddr,
		"method", req.Method,
		"url", req.URL.String(),
		"status", resp.Status,
		"status_code", resp.StatusCode,
		"duration", forwardDuration,
		"response_headers", utils.SanitizeHeaders(resp.Header))

	// 1. 先发送响应头
	headerBuf := new(bytes.Buffer)
	fmt.Fprintf(headerBuf, "HTTP/1.1 %s\r\n", resp.Status)
	_ = resp.Header.Write(headerBuf)
	headerBuf.WriteString("\r\n")

	headerMsg := protocol.TunnelMessage{ID: reqMsg.ID, Type: protocol.MSG_TYPE_HTTP_RES, Payload: headerBuf.Bytes()}
	headerData, _ := protocol.SerializeTunnelMessage(headerMsg)

	logger.Debug("Sending response header to server",
		"key", c.key,
		"request_id", reqMsg.ID,
		"header_size", len(headerData))

	select {
	case c.writeChan <- headerData:
		logger.Debug("Response header successfully queued for writing",
			"key", c.key,
			"request_id", reqMsg.ID)
	case <-time.After(10 * time.Second):
		logger.Error("Failed to queue response header for writing",
			"key", c.key,
			"request_id", reqMsg.ID,
			"timeout", "10s")
		return // 如果头都发不出去，后面的也没意义了
	}

	// 2. 流式发送响应体
	logger.Debug("Starting response body streaming",
		"key", c.key,
		"request_id", reqMsg.ID,
		"total_duration", time.Since(startTime))

	// streamResponseBody 函数内部会负责关闭 resp.Body
	go c.streamResponseBody(resp.Body, reqMsg.ID)
}

// streamResponseBody 流式地读取响应体并发送数据块
func (c *TunnelClient) streamResponseBody(body io.ReadCloser, requestID uint64) {
	defer body.Close()

	logger.Debug("Starting response body streaming",
		"key", c.key,
		"request_id", requestID)

	buf := make([]byte, 32*1024) // 32KB 的缓冲区
	totalBytes := 0
	chunkCount := 0

	for {
		n, err := body.Read(buf)
		if n > 0 {
			chunkCount++
			totalBytes += n

			logger.Debug("Read response body chunk",
				"key", c.key,
				"request_id", requestID,
				"chunk_size", n,
				"chunk_count", chunkCount,
				"total_bytes", totalBytes)

			chunkMsg := protocol.TunnelMessage{ID: requestID, Type: protocol.MSG_TYPE_HTTP_RES_CHUNK, Payload: buf[:n]}
			chunkData, _ := protocol.SerializeTunnelMessage(chunkMsg)

			select {
			case c.writeChan <- chunkData:
				logger.Debug("Response body chunk queued for writing",
					"key", c.key,
					"request_id", requestID,
					"chunk_count", chunkCount,
					"chunk_size", n)
			case <-c.closeChan:
				// 连接已关闭，退出
				logger.Warn("Connection closed while streaming body",
					"key", c.key,
					"request_id", requestID,
					"chunks_sent", chunkCount,
					"total_bytes", totalBytes)
				return
			}
		}

		if err != nil {
			if err != io.EOF {
				logger.Error("Error while reading response body",
					"key", c.key,
					"request_id", requestID,
					"chunks_sent", chunkCount,
					"total_bytes", totalBytes,
					"error", err)
			} else {
				logger.Debug("Finished reading response body",
					"key", c.key,
					"request_id", requestID,
					"chunks_sent", chunkCount,
					"total_bytes", totalBytes)
			}
			break // 读取完毕或出错，退出循环
		}
	}

	// 发送空数据块表示流结束
	logger.Debug("Sending end-of-stream marker",
		"key", c.key,
		"request_id", requestID,
		"total_chunks", chunkCount,
		"total_bytes", totalBytes)

	endMsg := protocol.TunnelMessage{ID: requestID, Type: protocol.MSG_TYPE_HTTP_RES_CHUNK, Payload: []byte{}}
	endData, _ := protocol.SerializeTunnelMessage(endMsg)

	select {
	case c.writeChan <- endData:
		logger.Info("Response body streaming completed",
			"key", c.key,
			"request_id", requestID,
			"total_chunks", chunkCount,
			"total_bytes", totalBytes)
	case <-c.closeChan:
		logger.Warn("Connection closed while sending end marker",
			"key", c.key,
			"request_id", requestID,
			"total_chunks", chunkCount,
			"total_bytes", totalBytes)
	}
}

func (c *TunnelClient) keepAlive() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.lastPingTime = time.Now()
			// 使用 WriteControl 来发送 Ping，它是线程安全的，不会与 writer goroutine 冲突
			if err := c.wsConn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				logger.Error("Keep-alive failed",
					"key", c.key,
					"error", err)
				return
			}
			logger.Debug("Sent ping to server at %s", c.lastPingTime.Format("15:04:05"))

			// 检查连接健康状态
			if !c.lastPongTime.IsZero() && time.Since(c.lastPongTime) > 45*time.Second {
				logger.Warn("WARNING: No pong received for %v, connection may be unhealthy", time.Since(c.lastPongTime))
			}
		case <-c.closeChan:
			return
		}
	}
}

// Connect 连接到服务器并建立隧道 (修改为非阻塞)
func (c *TunnelClient) Connect() error {
	// 确保 closeChan 已初始化
	if c.closeChan == nil {
		c.closeChan = make(chan struct{})
	}

	logger.Info("Attempting to connect to server",
		"server_addr", c.serverAddr.String(),
		"key", c.key,
		"target_addr", c.targetAddr,
		"reconnect_count", c.reconnectCount)

	// 在建立新连接前，确保旧的连接已关闭
	if c.wsConn != nil {
		logger.Debug("Closing existing WebSocket connection")
		c.wsConn.Close()
	}

	connURL := *c.serverAddr
	// 保留原始路径，并正确构造WebSocket端点路径
	basePath := connURL.Path
	if basePath == "" || basePath == "/" {
		connURL.Path = "/ws/" + c.key
	} else {
		// 移除末尾的斜杠，然后附加WebSocket路径
		if basePath[len(basePath)-1] == '/' {
			basePath = basePath[:len(basePath)-1]
		}
		connURL.Path = basePath + "/ws/" + c.key
	}

	logger.Debug("Preparing WebSocket connection",
		"url", connURL.String(),
		"tls_enabled", c.tlsConfig != nil)

	dialer := websocket.DefaultDialer
	dialer.TLSClientConfig = c.tlsConfig

	connectStart := time.Now()
	wsConn, response, err := dialer.Dial(connURL.String(), nil)
	if err != nil {
		logger.Error("Failed to connect to server",
			"server_addr", c.serverAddr.String(),
			"key", c.key,
			"duration", time.Since(connectStart),
			"error", err)
		return fmt.Errorf("failed to connect to server: %v", err)
	}

	c.wsConn = wsConn
	connectDuration := time.Since(connectStart)
	c.reconnectCount++

	logger.Info("Successfully connected to server",
		"server_addr", c.serverAddr.String(),
		"key", c.key,
		"target_addr", c.targetAddr,
		"duration", connectDuration,
		"response_status", response.Status,
		"reconnect_count", c.reconnectCount)

	// 启动后台goroutines
	logger.Debug("Starting background goroutines",
		"key", c.key,
		"goroutines", []string{"readLoop", "writer", "keepAlive"})
	go c.readLoop()
	go c.writer()
	go c.keepAlive()

	return nil
}

// Run 启动客户端并保持运行，支持自动重连 (修复版 - 添加指数退避)
func (c *TunnelClient) Run() {
	for {
		// 在每次尝试连接前，都创建一个新的 closeChan
		c.closeChan = make(chan struct{})
		logger.Info("Attempting to connect to the server... (attempt #%d)", c.reconnectCount+1)
		err := c.Connect()
		if err != nil {
			c.reconnectCount++
			// 指数退避：最小5秒，最大60秒
			delay := time.Duration(5+utils.Min(c.reconnectCount*2, 55)) * time.Second
			logger.Error("Connection failed: %v. Retrying in %v... (failed attempts: %d)", err, delay, c.reconnectCount)
			time.Sleep(delay)
			continue
		}

		// 连接成功，重置重连计数器
		if c.reconnectCount > 0 {
			logger.Info("Successfully reconnected after %d failed attempts", c.reconnectCount)
			c.reconnectCount = 0
		}

		logger.Info("Client is running. Waiting for disconnection...")
		// 阻塞，直到连接断开
		<-c.closeChan
		logger.Info("Connection lost. Preparing to reconnect...")
		c.reconnectCount++

		// 短暂延迟后重连
		time.Sleep(3 * time.Second)
	}
}
