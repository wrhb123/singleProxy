package server

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"singleproxy/pkg/logger"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/time/rate"

	"singleproxy/pkg/protocol"
)

// clientReadLoop 是唯一的读取器，处理来自客户端的所有消息 (支持流式传输)
func (p *SinglePortProxy) clientReadLoop(wsConn *websocket.Conn, key string) {
	remoteAddr := wsConn.RemoteAddr().String()

	logger.Info("Starting client read loop",
		"key", key,
		"remote_addr", remoteAddr)

	defer func() {
		wsConn.Close()
		p.connsMu.Lock()
		delete(p.clientConns, key)
		connectionCount := len(p.clientConns)
		p.connsMu.Unlock()

		logger.Info("Tunnel client disconnected",
			"key", key,
			"remote_addr", remoteAddr,
			"remaining_active_tunnels", connectionCount)
	}()

	wsConn.SetReadLimit(10 * 1024 * 1024)
	// 与客户端保持一致的超时时间
	serverReadTimeout := 90 * time.Second
	_ = wsConn.SetReadDeadline(time.Now().Add(serverReadTimeout))

	logger.Debug("Set WebSocket read configuration",
		"key", key,
		"read_limit", "10MB",
		"read_timeout", serverReadTimeout)

	wsConn.SetPongHandler(func(string) error {
		_ = wsConn.SetReadDeadline(time.Now().Add(serverReadTimeout))
		logger.Debug("Received pong from client",
			"key", key,
			"remote_addr", remoteAddr)
		return nil
	})

	messageCount := 0
	for {
		_, data, err := wsConn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				logger.Error("Unexpected WebSocket close error",
					"key", key,
					"remote_addr", remoteAddr,
					"error", err,
					"messages_processed", messageCount)
			} else {
				logger.Info("WebSocket connection closed",
					"key", key,
					"remote_addr", remoteAddr,
					"reason", err.Error(),
					"messages_processed", messageCount)
			}
			break
		}

		messageCount++
		logger.Debug("Received message from client",
			"key", key,
			"remote_addr", remoteAddr,
			"message_size", len(data),
			"total_messages", messageCount)

		msg, err := protocol.DeserializeTunnelMessage(data)
		if err != nil {
			logger.Error("Failed to deserialize tunnel message",
				"key", key,
				"remote_addr", remoteAddr,
				"message_size", len(data),
				"error", err)
			continue
		}

		logger.Debug("Deserialized tunnel message",
			"key", key,
			"remote_addr", remoteAddr,
			"message_id", msg.ID,
			"message_type", msg.Type,
			"payload_size", len(msg.Payload))

		p.handlersMu.Lock()
		handler, ok := p.streamHandlers[msg.ID]
		if !ok {
			// 如果找不到处理器，说明这是一个新的请求
			if msg.Type == protocol.MSG_TYPE_HTTP_RES {
				logger.Warn("Received response for unknown request ID",
					"key", key,
					"remote_addr", remoteAddr,
					"request_id", msg.ID,
					"message_type", msg.Type)
			}
			p.handlersMu.Unlock()
			continue
		}

		if msg.Type == protocol.MSG_TYPE_HTTP_RES {
			// 收到响应头
			logger.Debug("Processing HTTP response header",
				"key", key,
				"request_id", msg.ID,
				"payload_size", len(msg.Payload))

			resp, err := protocol.DeserializeHTTPResponse(msg.Payload)
			if err != nil {
				logger.Error("Failed to deserialize response header",
					"key", key,
					"request_id", msg.ID,
					"error", err)
				delete(p.streamHandlers, msg.ID)
				close(handler.done)
				p.handlersMu.Unlock()
				continue
			}

			logger.Debug("Sending HTTP response header to client",
				"key", key,
				"request_id", msg.ID,
				"status_code", resp.StatusCode,
				"header_count", len(resp.Header))

			// 将响应头写回给公网用户
			for k, v := range resp.Header {
				handler.writer.Header()[k] = v
			}
			handler.writer.WriteHeader(resp.StatusCode)
			handler.flusher.Flush() // 立即发送头部

		} else if msg.Type == protocol.MSG_TYPE_HTTP_RES_CHUNK {
			// 收到响应体数据块
			if len(msg.Payload) > 0 {
				logger.Debug("Processing response body chunk",
					"key", key,
					"request_id", msg.ID,
					"chunk_size", len(msg.Payload))

				if _, err := handler.writer.Write(msg.Payload); err != nil {
					logger.Error("Failed to write chunk to response",
						"key", key,
						"request_id", msg.ID,
						"chunk_size", len(msg.Payload),
						"error", err)
				}
				handler.flusher.Flush() // 立即发送数据块
			} else {
				// 收到空的数据块，表示流结束
				logger.Debug("Response body streaming finished",
					"key", key,
					"request_id", msg.ID)
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
	startTime := time.Now()

	// 检查 IP 速率限制
	ip, port, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		logger.Error("Failed to parse remote address",
			"remote_addr", r.RemoteAddr,
			"error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	logger.Debug("Processing public HTTP request",
		"client_ip", ip,
		"client_port", port,
		"method", r.Method,
		"url", r.URL.String(),
		"user_agent", r.Header.Get("User-Agent"))

	ipLimiter := p.getIPLimiter(ip)
	if !ipLimiter.Allow() {
		logger.Warn("IP rate limited",
			"client_ip", ip,
			"method", r.Method,
			"url", r.URL.String())
		http.Error(w, "Too many requests from your IP", http.StatusTooManyRequests)
		return
	}

	// 2. 获取密钥
	key := r.Header.Get("X-Tunnel-Key")
	if key == "" {
		key = "default"
		logger.Debug("Using default tunnel key", "client_ip", ip)
	} else {
		logger.Debug("Using tunnel key from header",
			"client_ip", ip,
			"key", key)
	}

	// 检查 Key 速率限制
	keyLimiter := p.getKeyLimiter(key)
	if !keyLimiter.Allow() {
		logger.Warn("Key rate limited",
			"client_ip", ip,
			"key", key,
			"method", r.Method,
			"url", r.URL.String())
		http.Error(w, "Too many requests for this service", http.StatusTooManyRequests)
		return
	}

	// 尝试WebSocket隧道
	p.connsMu.RLock()
	wsConn, wsExists := p.clientConns[key]
	p.connsMu.RUnlock()

	// 尝试HTTP长轮询隧道
	p.httpTunnelMgr.mu.RLock()
	httpClient, httpExists := p.httpTunnelMgr.clients[key]
	p.httpTunnelMgr.mu.RUnlock()

	if !wsExists && !httpExists {
		logger.Warn("No active tunnel for key",
			"client_ip", ip,
			"key", key,
			"method", r.Method,
			"url", r.URL.String(),
			"available_ws_keys", func() []string {
				p.connsMu.RLock()
				defer p.connsMu.RUnlock()
				keys := make([]string, 0, len(p.clientConns))
				for k := range p.clientConns {
					keys = append(keys, k)
				}
				return keys
			}(),
			"available_http_keys", func() []string {
				p.httpTunnelMgr.mu.RLock()
				defer p.httpTunnelMgr.mu.RUnlock()
				keys := make([]string, 0, len(p.httpTunnelMgr.clients))
				for k := range p.httpTunnelMgr.clients {
					keys = append(keys, k)
				}
				return keys
			}())
		http.Error(w, "Service unavailable", http.StatusBadGateway)
		return
	}

	// 序列化HTTP请求
	reqData, err := protocol.SerializeHTTPRequest(r)
	if err != nil {
		logger.Error("Failed to serialize request",
			"client_ip", ip,
			"key", key,
			"method", r.Method,
			"url", r.URL.String(),
			"error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	requestID := atomic.AddUint64(&p.nextRequestID, 1)

	logger.Debug("Generated request ID and serialized request",
		"client_ip", ip,
		"key", key,
		"request_id", requestID,
		"serialized_size", len(reqData),
		"method", r.Method,
		"url", r.URL.String())

	// 检查 ResponseWriter 是否支持 Flusher
	flusher, ok := w.(http.Flusher)
	if !ok {
		logger.Error("ResponseWriter does not support flushing",
			"client_ip", ip,
			"key", key,
			"request_id", requestID)
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

	tunnelMsg := protocol.TunnelMessage{ID: requestID, Type: protocol.MSG_TYPE_HTTP_REQ, Payload: reqData}

	// 选择隧道类型发送消息
	if wsExists {
		// 使用WebSocket隧道
		logger.Debug("Sending request to client via WebSocket",
			"client_ip", ip,
			"key", key,
			"request_id", requestID)

		msgData, _ := protocol.SerializeTunnelMessage(tunnelMsg)
		if err := wsConn.WriteMessage(websocket.BinaryMessage, msgData); err != nil {
			logger.Error("Failed to send request to WebSocket client",
				"client_ip", ip,
				"key", key,
				"request_id", requestID,
				"error", err)
			p.handlersMu.Lock()
			delete(p.streamHandlers, requestID)
			p.handlersMu.Unlock()
			http.Error(w, "Failed to forward request", http.StatusBadGateway)
			return
		}

		logger.Debug("Request sent to WebSocket client",
			"client_ip", ip,
			"key", key,
			"request_id", requestID)

	} else if httpExists {
		// 使用HTTP长轮询隧道
		logger.Debug("Sending request to client via HTTP tunnel",
			"client_ip", ip,
			"key", key,
			"request_id", requestID)

		// 发送消息到长轮询客户端
		select {
		case httpClient.pollChan <- &tunnelMsg:
			logger.Debug("Request queued for HTTP tunnel client",
				"client_ip", ip,
				"key", key,
				"request_id", requestID)
		default:
			// 通道已满，客户端可能无响应
			logger.Error("Failed to queue request for HTTP tunnel client - channel full",
				"client_ip", ip,
				"key", key,
				"request_id", requestID)
			p.handlersMu.Lock()
			delete(p.streamHandlers, requestID)
			p.handlersMu.Unlock()
			http.Error(w, "Tunnel client busy", http.StatusServiceUnavailable)
			return
		}
	}

	// 等待流结束或超时 (增加更长的超时时间，避免与连接超时冲突)
	timeout := 90 * time.Second
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-handler.done:
		// 流正常结束
		duration := time.Since(startTime)
		tunnelType := "WebSocket"
		if httpExists && !wsExists {
			tunnelType = "HTTP"
		}
		logger.Info("Response stream completed successfully",
			"client_ip", ip,
			"key", key,
			"request_id", requestID,
			"duration", duration,
			"method", r.Method,
			"url", r.URL.String(),
			"tunnel_type", tunnelType)
	case <-timer.C:
		duration := time.Since(startTime)
		logger.Error("Timeout waiting for response stream",
			"client_ip", ip,
			"key", key,
			"request_id", requestID,
			"timeout", timeout,
			"duration", duration,
			"method", r.Method,
			"url", r.URL.String())
		p.handlersMu.Lock()
		delete(p.streamHandlers, requestID)
		p.handlersMu.Unlock()
		http.Error(w, "Gateway Timeout", http.StatusGatewayTimeout)
	}
}

// handleHTTPTunnel 处理HTTP长轮询模式的隧道连接
func (p *SinglePortProxy) handleHTTPTunnel(w http.ResponseWriter, r *http.Request) {
	// 解析路径获取操作类型和key
	pathParts := strings.Split(strings.TrimPrefix(r.URL.Path, "/http-tunnel/"), "/")
	if len(pathParts) < 2 {
		http.Error(w, "Invalid HTTP tunnel path format. Use: /http-tunnel/{operation}/{key}", http.StatusBadRequest)
		return
	}

	operation := pathParts[0]
	key := pathParts[1]

	if key == "" {
		http.Error(w, "Tunnel key cannot be empty", http.StatusBadRequest)
		return
	}

	logger.Debug("Processing HTTP tunnel request",
		"operation", operation,
		"key", key,
		"method", r.Method,
		"remote_addr", r.RemoteAddr)

	switch operation {
	case "register":
		p.handleHTTPTunnelRegister(w, r, key)
	case "poll":
		p.handleHTTPTunnelPoll(w, r, key)
	case "response":
		p.handleHTTPTunnelResponse(w, r, key)
	default:
		http.Error(w, "Invalid operation. Use: register, poll, or response", http.StatusBadRequest)
	}
}

// handleHTTPTunnelRegister 处理客户端注册请求
func (p *SinglePortProxy) handleHTTPTunnelRegister(w http.ResponseWriter, r *http.Request, key string) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed. Use POST", http.StatusMethodNotAllowed)
		return
	}

	remoteAddr := r.RemoteAddr
	logger.Info("HTTP tunnel client registering",
		"key", key,
		"remote_addr", remoteAddr)

	// 创建或更新客户端
	p.httpTunnelMgr.mu.Lock()

	// 清理旧的客户端连接（如果存在）
	if oldClient, exists := p.httpTunnelMgr.clients[key]; exists {
		close(oldClient.pollChan)
		close(oldClient.responseChan)
		logger.Info("Replacing existing HTTP tunnel client",
			"key", key,
			"old_remote_addr", oldClient.remoteAddr,
			"new_remote_addr", remoteAddr)
	}

	// 创建新的客户端
	client := &httpTunnelClient{
		key:          key,
		remoteAddr:   remoteAddr,
		lastSeen:     time.Now(),
		pollChan:     make(chan *protocol.TunnelMessage, 10), // 缓冲通道
		responseChan: make(chan *protocol.TunnelMessage, 10),
	}
	p.httpTunnelMgr.clients[key] = client
	clientCount := len(p.httpTunnelMgr.clients)
	p.httpTunnelMgr.mu.Unlock()

	// 启动客户端清理协程
	go p.cleanupHTTPTunnelClient(key)

	logger.Info("HTTP tunnel client registered successfully",
		"key", key,
		"remote_addr", remoteAddr,
		"total_active_tunnels", clientCount)

	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status": "registered", "message": "HTTP tunnel registered successfully"}`))
}

// handleHTTPTunnelPoll 处理客户端长轮询请求
func (p *SinglePortProxy) handleHTTPTunnelPoll(w http.ResponseWriter, r *http.Request, key string) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed. Use GET", http.StatusMethodNotAllowed)
		return
	}

	p.httpTunnelMgr.mu.RLock()
	client, exists := p.httpTunnelMgr.clients[key]
	p.httpTunnelMgr.mu.RUnlock()

	if !exists {
		http.Error(w, "Tunnel not registered. Please register first", http.StatusNotFound)
		return
	}

	// 更新最后见到时间
	p.httpTunnelMgr.mu.Lock()
	client.lastSeen = time.Now()
	p.httpTunnelMgr.mu.Unlock()

	logger.Debug("HTTP tunnel client polling for messages",
		"key", key,
		"remote_addr", r.RemoteAddr)

	// 长轮询：等待消息或超时
	timeout := 30 * time.Second
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	w.Header().Set("Content-Type", "application/json")

	select {
	case msg := <-client.pollChan:
		// 收到消息，立即返回
		msgData, err := protocol.SerializeTunnelMessage(*msg)
		if err != nil {
			logger.Error("Failed to serialize tunnel message",
				"key", key,
				"error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write(msgData)

		logger.Debug("HTTP tunnel message sent to client",
			"key", key,
			"message_id", msg.ID,
			"message_type", msg.Type)

	case <-timer.C:
		// 超时，返回空响应
		w.WriteHeader(http.StatusNoContent)
		logger.Debug("HTTP tunnel poll timeout",
			"key", key,
			"timeout", timeout)

	case <-r.Context().Done():
		// 客户端取消请求
		logger.Debug("HTTP tunnel poll cancelled by client",
			"key", key)
		return
	}
}

// handleHTTPTunnelResponse 处理客户端响应
func (p *SinglePortProxy) handleHTTPTunnelResponse(w http.ResponseWriter, r *http.Request, key string) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed. Use POST", http.StatusMethodNotAllowed)
		return
	}

	p.httpTunnelMgr.mu.RLock()
	client, exists := p.httpTunnelMgr.clients[key]
	p.httpTunnelMgr.mu.RUnlock()

	if !exists {
		http.Error(w, "Tunnel not registered. Please register first", http.StatusNotFound)
		return
	}

	// 读取响应数据
	body, err := io.ReadAll(r.Body)
	if err != nil {
		logger.Error("Failed to read response body",
			"key", key,
			"error", err)
		http.Error(w, "Failed to read response body", http.StatusBadRequest)
		return
	}

	// 反序列化消息
	msg, err := protocol.DeserializeTunnelMessage(body)
	if err != nil {
		logger.Error("Failed to deserialize tunnel message",
			"key", key,
			"error", err)
		http.Error(w, "Invalid message format", http.StatusBadRequest)
		return
	}

	// 更新最后见到时间
	p.httpTunnelMgr.mu.Lock()
	client.lastSeen = time.Now()
	p.httpTunnelMgr.mu.Unlock()

	logger.Debug("HTTP tunnel response received",
		"key", key,
		"message_id", msg.ID,
		"message_type", msg.Type)

	// 处理响应消息
	p.handleHTTPTunnelMessage(&msg, key)

	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status": "received"}`))
}

// cleanupHTTPTunnelClient 定期清理不活跃的客户端
func (p *SinglePortProxy) cleanupHTTPTunnelClient(key string) {
	ticker := time.NewTicker(60 * time.Second) // 每分钟检查一次
	defer ticker.Stop()

	for range ticker.C {
		p.httpTunnelMgr.mu.Lock()
		client, exists := p.httpTunnelMgr.clients[key]
		if !exists {
			p.httpTunnelMgr.mu.Unlock()
			return // 客户端已被删除，退出清理协程
		}

		// 检查客户端是否超时（5分钟无活动）
		if time.Since(client.lastSeen) > 5*time.Minute {
			logger.Info("Cleaning up inactive HTTP tunnel client",
				"key", key,
				"last_seen", client.lastSeen,
				"inactive_duration", time.Since(client.lastSeen))

			close(client.pollChan)
			close(client.responseChan)
			delete(p.httpTunnelMgr.clients, key)
			p.httpTunnelMgr.mu.Unlock()
			return
		}
		p.httpTunnelMgr.mu.Unlock()
	}
}

// handleHTTPTunnelMessage 处理来自HTTP长轮询客户端的响应消息
func (p *SinglePortProxy) handleHTTPTunnelMessage(msg *protocol.TunnelMessage, key string) {
	logger.Debug("Processing HTTP tunnel message",
		"key", key,
		"message_id", msg.ID,
		"message_type", msg.Type)

	switch msg.Type {
	case protocol.MSG_TYPE_HTTP_RES:
		// HTTP响应消息
		p.handlersMu.Lock()
		handler, ok := p.streamHandlers[msg.ID]
		if !ok {
			p.handlersMu.Unlock()
			logger.Warn("No handler found for HTTP response",
				"key", key,
				"message_id", msg.ID)
			return
		}
		p.handlersMu.Unlock()

		// 反序列化HTTP响应
		resp, err := protocol.DeserializeHTTPResponse(msg.Payload)
		if err != nil {
			logger.Error("Failed to deserialize HTTP response",
				"key", key,
				"message_id", msg.ID,
				"error", err)
			close(handler.done)
			return
		}

		// 写入响应头
		for key, values := range resp.Header {
			for _, value := range values {
				handler.writer.Header().Add(key, value)
			}
		}

		// 写入状态码
		handler.writer.WriteHeader(resp.StatusCode)

		// 写入响应体
		if resp.Body != nil {
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				logger.Error("Failed to read response body",
					"key", key,
					"message_id", msg.ID,
					"error", err)
			} else if len(body) > 0 {
				_, err = handler.writer.Write(body)
				if err != nil {
					logger.Error("Failed to write response body",
						"key", key,
						"message_id", msg.ID,
						"error", err)
				}
			}
			resp.Body.Close()
		}

		// 完成响应
		handler.flusher.Flush()
		close(handler.done)

		logger.Debug("HTTP tunnel response completed",
			"key", key,
			"message_id", msg.ID,
			"status_code", resp.StatusCode)

	case protocol.MSG_TYPE_HTTP_RES_CHUNK:
		// HTTP响应数据块
		p.handlersMu.Lock()
		handler, ok := p.streamHandlers[msg.ID]
		if !ok {
			p.handlersMu.Unlock()
			logger.Warn("No handler found for HTTP response chunk",
				"key", key,
				"message_id", msg.ID)
			return
		}
		p.handlersMu.Unlock()

		// 写入数据块
		if len(msg.Payload) > 0 {
			_, err := handler.writer.Write(msg.Payload)
			if err != nil {
				logger.Error("Failed to write response chunk",
					"key", key,
					"message_id", msg.ID,
					"error", err)
				close(handler.done)
				return
			}
			handler.flusher.Flush()
		}

		logger.Debug("HTTP tunnel response chunk written",
			"key", key,
			"message_id", msg.ID,
			"chunk_size", len(msg.Payload))

	default:
		logger.Warn("Unknown HTTP tunnel message type",
			"key", key,
			"message_id", msg.ID,
			"message_type", msg.Type)
	}
}

// handleHTTPProxy 处理基于路径的HTTP代理请求
func (p *SinglePortProxy) handleHTTPProxy(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()

	// 检查 IP 速率限制
	ip, port, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		logger.Error("Failed to parse remote address for proxy",
			"remote_addr", r.RemoteAddr,
			"error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	logger.Debug("Processing HTTP path proxy request",
		"client_ip", ip,
		"client_port", port,
		"method", r.Method,
		"url", r.URL.String(),
		"user_agent", r.Header.Get("User-Agent"))

	ipLimiter := p.getIPLimiter(ip)
	if !ipLimiter.Allow() {
		logger.Warn("IP rate limited for proxy request",
			"client_ip", ip,
			"method", r.Method,
			"url", r.URL.String())
		http.Error(w, "Too many requests from your IP", http.StatusTooManyRequests)
		return
	}

	// 只支持基于路径的代理请求：/proxy/host:port/path
	if !strings.HasPrefix(r.URL.Path, "/proxy/") {
		logger.Error("Invalid proxy path format",
			"client_ip", ip,
			"path", r.URL.Path)
		http.Error(w, "Invalid proxy path format. Use: /proxy/host:port/path", http.StatusBadRequest)
		return
	}

	// 解析路径：/proxy/host:port/path
	pathParts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/proxy/"), "/", 2)
	if len(pathParts) == 0 || pathParts[0] == "" {
		logger.Error("Invalid proxy path format",
			"client_ip", ip,
			"path", r.URL.Path)
		http.Error(w, "Invalid proxy path format. Use: /proxy/host:port/path", http.StatusBadRequest)
		return
	}

	hostPort := pathParts[0]
	var targetHost, targetPort string

	if strings.Contains(hostPort, ":") {
		targetHost, targetPort, err = net.SplitHostPort(hostPort)
		if err != nil {
			logger.Error("Invalid proxy target in path",
				"client_ip", ip,
				"host_port", hostPort,
				"error", err)
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}
	} else {
		targetHost = hostPort
		targetPort = "80" // 默认HTTP端口
	}

	// 重写请求路径，去掉代理前缀
	if len(pathParts) > 1 {
		r.URL.Path = "/" + pathParts[1]
	} else {
		r.URL.Path = "/"
	}
	r.URL.Host = net.JoinHostPort(targetHost, targetPort)
	r.Host = r.URL.Host

	logger.Info("HTTP path proxy connection established",
		"client_ip", ip,
		"target_host", targetHost,
		"target_port", targetPort,
		"method", r.Method)

	// 连接到目标服务器
	targetAddr := net.JoinHostPort(targetHost, targetPort)
	targetConn, err := net.DialTimeout("tcp", targetAddr, 30*time.Second)
	if err != nil {
		logger.Error("Failed to connect to target server",
			"client_ip", ip,
			"target_addr", targetAddr,
			"error", err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}
	defer targetConn.Close()

	logger.Debug("Successfully connected to target server",
		"client_ip", ip,
		"target_addr", targetAddr)

	// 转发HTTP请求到目标服务器
	logger.Debug("Forwarding HTTP request to target",
		"client_ip", ip,
		"target_addr", targetAddr,
		"method", r.Method,
		"path", r.URL.Path)

	// 转发请求到目标服务器
	err = r.Write(targetConn)
	if err != nil {
		logger.Error("Failed to forward request to target",
			"client_ip", ip,
			"target_addr", targetAddr,
			"error", err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}

	// 读取目标服务器的响应
	targetReader := bufio.NewReader(targetConn)
	resp, err := http.ReadResponse(targetReader, r)
	if err != nil {
		logger.Error("Failed to read response from target",
			"client_ip", ip,
			"target_addr", targetAddr,
			"error", err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// 复制响应头
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	// 写入状态码
	w.WriteHeader(resp.StatusCode)

	// 复制响应体
	_, err = io.Copy(w, resp.Body)
	duration := time.Since(startTime)

	if err != nil {
		logger.Error("Failed to copy response body",
			"client_ip", ip,
			"target_addr", targetAddr,
			"duration", duration,
			"error", err)
	} else {
		logger.Info("HTTP path proxy request completed successfully",
			"client_ip", ip,
			"target_addr", targetAddr,
			"method", r.Method,
			"status_code", resp.StatusCode,
			"duration", duration)
	}
}
