package server

import (
	"net"
	"net/http"
	"singleproxy/pkg/logger"
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

	p.connsMu.RLock()
	wsConn, ok := p.clientConns[key]
	p.connsMu.RUnlock()

	if !ok {
		logger.Warn("No active tunnel for key",
			"client_ip", ip,
			"key", key,
			"method", r.Method,
			"url", r.URL.String(),
			"available_keys", func() []string {
				p.connsMu.RLock()
				defer p.connsMu.RUnlock()
				keys := make([]string, 0, len(p.clientConns))
				for k := range p.clientConns {
					keys = append(keys, k)
				}
				return keys
			}())
		http.Error(w, "Service unavailable", http.StatusBadGateway)
		return
	}

	logger.Debug("Found active tunnel connection",
		"client_ip", ip,
		"key", key,
		"method", r.Method,
		"url", r.URL.String())

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
	msgData, _ := protocol.SerializeTunnelMessage(tunnelMsg)

	logger.Debug("Sending request to client via WebSocket",
		"client_ip", ip,
		"key", key,
		"request_id", requestID,
		"tunnel_message_size", len(msgData))

	if err := wsConn.WriteMessage(websocket.BinaryMessage, msgData); err != nil {
		logger.Error("Failed to send request to client",
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

	logger.Debug("Request sent to client, waiting for response",
		"client_ip", ip,
		"key", key,
		"request_id", requestID)

	// 等待流结束或超时 (增加更长的超时时间，避免与连接超时冲突)
	timeout := 90 * time.Second
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-handler.done:
		// 流正常结束
		duration := time.Since(startTime)
		logger.Info("Response stream completed successfully",
			"client_ip", ip,
			"key", key,
			"request_id", requestID,
			"duration", duration,
			"method", r.Method,
			"url", r.URL.String())
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
