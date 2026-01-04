package client

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"singleproxy/pkg/config"
	"singleproxy/pkg/logger"
	"singleproxy/pkg/protocol"
)

// HTTPTunnelClient HTTP长轮询隧道客户端
type HTTPTunnelClient struct {
	serverURL string
	key       string
	target    string
	client    *http.Client
	insecure  bool
}

// NewHTTPTunnelClient 创建HTTP长轮询客户端
func NewHTTPTunnelClient(cfg *config.Config) (*HTTPTunnelClient, error) {
	if cfg.ServerAddr == "" {
		return nil, fmt.Errorf("server address cannot be empty")
	}
	if cfg.TargetAddr == "" {
		return nil, fmt.Errorf("target address cannot be empty")
	}
	if cfg.Key == "" {
		return nil, fmt.Errorf("tunnel key cannot be empty")
	}

	// 解析服务器URL以确定是否使用HTTPS
	serverURL, err := url.Parse(cfg.ServerAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid server URL: %v", err)
	}

	// 创建HTTP客户端，配置TLS设置
	transport := &http.Transport{
		TLSHandshakeTimeout: 10 * time.Second,
		IdleConnTimeout:     90 * time.Second,
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 5,
	}

	// 如果是HTTPS连接，配置TLS
	if serverURL.Scheme == "https" {
		transport.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: cfg.Insecure,
		}

		logger.Info("HTTP tunnel client TLS configuration",
			"server_url", cfg.ServerAddr,
			"insecure_skip_verify", cfg.Insecure,
			"key", cfg.Key)
	}

	httpClient := &http.Client{
		Timeout:   65 * time.Second, // 长轮询超时时间稍长于服务器
		Transport: transport,
	}

	return &HTTPTunnelClient{
		serverURL: cfg.ServerAddr,
		key:       cfg.Key,
		target:    cfg.TargetAddr,
		client:    httpClient,
		insecure:  cfg.Insecure,
	}, nil
}

// Register 注册隧道
func (c *HTTPTunnelClient) Register() error {
	url := fmt.Sprintf("%s/http-tunnel/register/%s", c.serverURL, c.key)

	resp, err := c.client.Post(url, "application/json", nil)
	if err != nil {
		return fmt.Errorf("failed to register: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("registration failed: %s", body)
	}

	logger.Info("HTTP tunnel registered successfully", "key", c.key)
	return nil
}

// StartPolling 开始长轮询循环
func (c *HTTPTunnelClient) StartPolling() {
	logger.Info("Starting HTTP tunnel polling", "key", c.key)

	for {
		err := c.pollOnce()
		if err != nil {
			logger.Error("Polling error", "error", err, "key", c.key)
			logger.Info("Retrying in 5 seconds...")
			time.Sleep(5 * time.Second)
			continue
		}
	}
}

// pollOnce 执行一次轮询
func (c *HTTPTunnelClient) pollOnce() error {
	url := fmt.Sprintf("%s/http-tunnel/poll/%s", c.serverURL, c.key)

	resp, err := c.client.Get(url)
	if err != nil {
		return fmt.Errorf("poll request failed: %v", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// 收到消息
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read poll response: %v", err)
		}

		msg, err := protocol.DeserializeTunnelMessage(body)
		if err != nil {
			return fmt.Errorf("failed to deserialize message: %v", err)
		}

		logger.Debug("Received message", "id", msg.ID, "type", msg.Type)
		return c.handleMessage(msg)

	case http.StatusNoContent:
		// 轮询超时，正常情况
		logger.Debug("Poll timeout, retrying...")
		return nil

	case http.StatusNotFound:
		// 隧道未注册
		logger.Info("Tunnel not registered, re-registering...")
		return c.Register()

	default:
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, body)
	}
}

// handleMessage 处理收到的消息
func (c *HTTPTunnelClient) handleMessage(msg protocol.TunnelMessage) error {
	switch msg.Type {
	case protocol.MSG_TYPE_HTTP_REQ:
		return c.handleHTTPRequest(msg)
	default:
		logger.Warn("Unknown message type", "type", msg.Type)
		return nil
	}
}

// handleHTTPRequest 处理HTTP请求
func (c *HTTPTunnelClient) handleHTTPRequest(msg protocol.TunnelMessage) error {
	// 解析HTTP请求
	req, err := protocol.ParseHTTPRequest(msg.Payload)
	if err != nil {
		logger.Error("Failed to parse HTTP request", "error", err)
		return c.sendErrorResponse(msg.ID, "Bad Request")
	}

	logger.Debug("Processing HTTP request", "method", req.Method, "path", req.URL.Path)

	// 转发到本地目标服务
	targetURL := fmt.Sprintf("http://%s%s", c.target, req.URL.RequestURI())

	// 创建转发请求
	targetReq, err := http.NewRequest(req.Method, targetURL, req.Body)
	if err != nil {
		logger.Error("Failed to create target request", "error", err)
		return c.sendErrorResponse(msg.ID, "Internal Server Error")
	}

	// 复制头部
	for key, values := range req.Header {
		for _, value := range values {
			targetReq.Header.Add(key, value)
		}
	}

	// 发送请求
	// 创建专用的转发客户端，复用TLS配置
	forwardClient := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSHandshakeTimeout: 10 * time.Second,
			IdleConnTimeout:     30 * time.Second,
			MaxIdleConns:        5,
			MaxIdleConnsPerHost: 2,
		},
	}

	resp, err := forwardClient.Do(targetReq)
	if err != nil {
		logger.Error("Failed to forward request", "error", err)
		return c.sendErrorResponse(msg.ID, "Bad Gateway")
	}
	defer resp.Body.Close()

	logger.Debug("Response received", "status", resp.StatusCode, "status_text", resp.Status)

	// 序列化响应
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "HTTP/1.1 %s\r\n", resp.Status)
	resp.Header.Write(&buf)
	buf.WriteString("\r\n")

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Error("Failed to read response body", "error", err)
		return c.sendErrorResponse(msg.ID, "Internal Server Error")
	}
	buf.Write(body)

	// 发送响应
	return c.sendResponse(msg.ID, buf.Bytes())
}

// sendResponse 发送响应
func (c *HTTPTunnelClient) sendResponse(requestID uint64, respData []byte) error {
	msg := protocol.TunnelMessage{
		ID:      requestID,
		Type:    protocol.MSG_TYPE_HTTP_RES,
		Payload: respData,
	}

	msgData, err := protocol.SerializeTunnelMessage(msg)
	if err != nil {
		return fmt.Errorf("failed to serialize response: %v", err)
	}

	url := fmt.Sprintf("%s/http-tunnel/response/%s", c.serverURL, c.key)
	resp, err := c.client.Post(url, "application/octet-stream", bytes.NewReader(msgData))
	if err != nil {
		return fmt.Errorf("failed to send response: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("response rejected: %s", body)
	}

	logger.Debug("Response sent", "request_id", requestID)
	return nil
}

// sendErrorResponse 发送错误响应
func (c *HTTPTunnelClient) sendErrorResponse(requestID uint64, errorMsg string) error {
	respData := fmt.Sprintf("HTTP/1.1 500 Internal Server Error\r\nContent-Type: text/plain\r\nContent-Length: %d\r\n\r\n%s",
		len(errorMsg), errorMsg)
	return c.sendResponse(requestID, []byte(respData))
}

// Run 启动客户端
func (c *HTTPTunnelClient) Run() error {
	// 首先注册
	if err := c.Register(); err != nil {
		return err
	}

	// 开始轮询
	c.StartPolling()
	return nil
}
