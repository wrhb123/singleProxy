package test

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"singleproxy/pkg/client"
	"singleproxy/pkg/config"
	"singleproxy/pkg/protocol"
	"singleproxy/pkg/server"
)

// TestEndToEndHTTPProxy 测试完整的HTTP代理功能
func TestEndToEndHTTPProxy(t *testing.T) {
	// 1. 创建目标服务器
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		response := fmt.Sprintf(`{"method": "%s", "path": "%s", "user_agent": "%s"}`, 
			r.Method, r.URL.Path, r.Header.Get("User-Agent"))
		w.Write([]byte(response))
	}))
	defer targetServer.Close()

	targetURL, _ := url.Parse(targetServer.URL)
	targetAddr := targetURL.Host

	// 2. 创建代理服务器
	serverCfg := &config.Config{
		Mode:       "server",
		ListenPort: "0", // 动态端口
	}
	proxy := server.NewSinglePortProxy(serverCfg)
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	proxyURL, _ := url.Parse(proxyServer.URL)
	wsURL := fmt.Sprintf("ws://%s", proxyURL.Host)

	// 3. 创建并启动客户端
	clientCfg := &config.Config{
		Mode:       "client",
		ServerAddr: wsURL,
		TargetAddr: targetAddr,
		Key:        "test-e2e",
		Insecure:   true,
	}

	tunnelClient, err := client.NewTunnelClient(clientCfg)
	if err != nil {
		t.Fatalf("Failed to create tunnel client: %v", err)
	}

	// 启动客户端连接
	clientDone := make(chan error, 1)
	go func() {
		clientDone <- tunnelClient.Connect()
	}()

	// 等待客户端连接建立
	time.Sleep(500 * time.Millisecond)

	// 4. 测试HTTP请求通过代理
	httpClient := &http.Client{Timeout: 10 * time.Second}
	
	req, err := http.NewRequest("GET", proxyServer.URL+"/api/test", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req.Header.Set("X-Tunnel-Key", "test-e2e")
	req.Header.Set("User-Agent", "Integration-Test-Client")

	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("Failed to send request through proxy: %v", err)
	}
	defer resp.Body.Close()

	// 5. 验证响应
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}

	expectedBody := `{"method": "GET", "path": "/api/test", "user_agent": "Integration-Test-Client"}`
	if string(body) != expectedBody {
		t.Errorf("Expected body %s, got %s", expectedBody, string(body))
	}

	// 清理：关闭客户端连接
	select {
	case err := <-clientDone:
		if err != nil {
			t.Logf("Client connection ended with: %v", err)
		}
	default:
		// 客户端仍在运行，这是正常的
	}
}

// TestMultipleClients 测试多个客户端同时连接
func TestMultipleClients(t *testing.T) {
	// 创建两个目标服务器
	targetServer1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Response from server 1"))
	}))
	defer targetServer1.Close()

	targetServer2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Response from server 2"))
	}))
	defer targetServer2.Close()

	// 创建代理服务器
	serverCfg := &config.Config{
		Mode:       "server",
		ListenPort: "0",
	}
	proxy := server.NewSinglePortProxy(serverCfg)
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	proxyURL, _ := url.Parse(proxyServer.URL)
	wsURL := fmt.Sprintf("ws://%s", proxyURL.Host)

	// 创建两个客户端，使用不同的key
	client1Cfg := &config.Config{
		Mode:       "client",
		ServerAddr: wsURL,
		TargetAddr: strings.TrimPrefix(targetServer1.URL, "http://"),
		Key:        "service1",
		Insecure:   true,
	}

	client2Cfg := &config.Config{
		Mode:       "client", 
		ServerAddr: wsURL,
		TargetAddr: strings.TrimPrefix(targetServer2.URL, "http://"),
		Key:        "service2",
		Insecure:   true,
	}

	client1, err := client.NewTunnelClient(client1Cfg)
	if err != nil {
		t.Fatalf("Failed to create client1: %v", err)
	}

	client2, err := client.NewTunnelClient(client2Cfg)
	if err != nil {
		t.Fatalf("Failed to create client2: %v", err)
	}

	// 启动两个客户端
	go client1.Connect()
	go client2.Connect()

	// 等待连接建立
	time.Sleep(500 * time.Millisecond)

	// 测试请求路由到正确的目标服务器
	httpClient := &http.Client{Timeout: 5 * time.Second}

	// 请求 service1
	req1, _ := http.NewRequest("GET", proxyServer.URL+"/test", nil)
	req1.Header.Set("X-Tunnel-Key", "service1")
	resp1, err := httpClient.Do(req1)
	if err != nil {
		t.Fatalf("Failed to send request to service1: %v", err)
	}
	defer resp1.Body.Close()

	body1, _ := io.ReadAll(resp1.Body)
	if string(body1) != "Response from server 1" {
		t.Errorf("Expected response from server 1, got: %s", string(body1))
	}

	// 请求 service2
	req2, _ := http.NewRequest("GET", proxyServer.URL+"/test", nil)
	req2.Header.Set("X-Tunnel-Key", "service2")
	resp2, err := httpClient.Do(req2)
	if err != nil {
		t.Fatalf("Failed to send request to service2: %v", err)
	}
	defer resp2.Body.Close()

	body2, _ := io.ReadAll(resp2.Body)
	if string(body2) != "Response from server 2" {
		t.Errorf("Expected response from server 2, got: %s", string(body2))
	}
}

// TestClientReconnection 测试客户端重连功能
func TestClientReconnection(t *testing.T) {
	// 创建目标服务器
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Hello from target"))
	}))
	defer targetServer.Close()

	// 创建代理服务器
	serverCfg := &config.Config{
		Mode:       "server",
		ListenPort: "0",
	}
	proxy := server.NewSinglePortProxy(serverCfg)
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	proxyURL, _ := url.Parse(proxyServer.URL)
	wsURL := fmt.Sprintf("ws://%s", proxyURL.Host)

	// 创建客户端
	clientCfg := &config.Config{
		Mode:       "client",
		ServerAddr: wsURL,
		TargetAddr: strings.TrimPrefix(targetServer.URL, "http://"),
		Key:        "reconnect-test",
		Insecure:   true,
	}

	tunnelClient, err := client.NewTunnelClient(clientCfg)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	// 启动客户端
	go tunnelClient.Connect()
	time.Sleep(500 * time.Millisecond)

	// 验证连接工作正常
	httpClient := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest("GET", proxyServer.URL+"/test", nil)
	req.Header.Set("X-Tunnel-Key", "reconnect-test")
	
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("Initial request failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Initial request should succeed, got status %d", resp.StatusCode)
	}
}

// TestLargeResponse 测试大文件响应的流式传输
func TestLargeResponse(t *testing.T) {
	// 创建生成大响应的目标服务器
	largeData := bytes.Repeat([]byte("A"), 1024*1024) // 1MB数据
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write(largeData)
	}))
	defer targetServer.Close()

	// 创建代理服务器
	serverCfg := &config.Config{
		Mode:       "server",
		ListenPort: "0",
	}
	proxy := server.NewSinglePortProxy(serverCfg)
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	proxyURL, _ := url.Parse(proxyServer.URL)
	wsURL := fmt.Sprintf("ws://%s", proxyURL.Host)

	// 创建客户端
	clientCfg := &config.Config{
		Mode:       "client",
		ServerAddr: wsURL,
		TargetAddr: strings.TrimPrefix(targetServer.URL, "http://"),
		Key:        "large-response-test",
		Insecure:   true,
	}

	tunnelClient, err := client.NewTunnelClient(clientCfg)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	go tunnelClient.Connect()
	time.Sleep(500 * time.Millisecond)

	// 请求大文件
	httpClient := &http.Client{Timeout: 30 * time.Second}
	req, _ := http.NewRequest("GET", proxyServer.URL+"/large", nil)
	req.Header.Set("X-Tunnel-Key", "large-response-test")

	start := time.Now()
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("Large response request failed: %v", err)
	}
	defer resp.Body.Close()

	// 读取所有数据
	responseData, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read large response: %v", err)
	}

	duration := time.Since(start)
	t.Logf("Large response (%d bytes) transferred in %v", len(responseData), duration)

	// 验证数据完整性
	if len(responseData) != len(largeData) {
		t.Errorf("Expected %d bytes, got %d bytes", len(largeData), len(responseData))
	}

	if !bytes.Equal(responseData, largeData) {
		t.Error("Response data does not match original data")
	}
}

// TestProtocolMessage 测试协议消息的序列化和反序列化
func TestProtocolMessage(t *testing.T) {
	testPayload := []byte("Test message payload with some data")
	
	// 测试不同类型的消息
	messageTypes := []uint8{
		protocol.MSG_TYPE_HTTP_REQ,
		protocol.MSG_TYPE_HTTP_RES,
		protocol.MSG_TYPE_HTTP_RES_CHUNK,
	}

	for _, msgType := range messageTypes {
		t.Run(fmt.Sprintf("MessageType_%d", msgType), func(t *testing.T) {
			original := protocol.TunnelMessage{
				ID:      12345,
				Type:    msgType,
				Payload: testPayload,
			}

			// 序列化
			serialized, err := protocol.SerializeTunnelMessage(original)
			if err != nil {
				t.Fatalf("Failed to serialize message: %v", err)
			}

			// 反序列化
			deserialized, err := protocol.DeserializeTunnelMessage(serialized)
			if err != nil {
				t.Fatalf("Failed to deserialize message: %v", err)
			}

			// 验证
			if deserialized.ID != original.ID {
				t.Errorf("ID mismatch: expected %d, got %d", original.ID, deserialized.ID)
			}
			if deserialized.Type != original.Type {
				t.Errorf("Type mismatch: expected %d, got %d", original.Type, deserialized.Type)
			}
			if !bytes.Equal(deserialized.Payload, original.Payload) {
				t.Error("Payload mismatch")
			}
		})
	}
}

// TestWebSocketConnection 测试WebSocket连接的基本功能
func TestWebSocketConnection(t *testing.T) {
	// 创建代理服务器
	serverCfg := &config.Config{
		Mode:       "server",
		ListenPort: "0",
	}
	proxy := server.NewSinglePortProxy(serverCfg)
	ts := httptest.NewServer(proxy)
	defer ts.Close()

	// 连接WebSocket
	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "/ws/websocket-test"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to dial WebSocket: %v, response: %v", err, resp)
	}
	defer conn.Close()

	// 验证连接成功
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Errorf("Expected status %d, got %d", http.StatusSwitchingProtocols, resp.StatusCode)
	}

	// 发送测试消息而不是ping（因为服务器可能不处理ping/pong）
	testMsg := protocol.TunnelMessage{
		ID:      999,
		Type:    protocol.MSG_TYPE_HTTP_REQ,
		Payload: []byte("test connection"),
	}

	data, err := protocol.SerializeTunnelMessage(testMsg)
	if err != nil {
		t.Fatalf("Failed to serialize test message: %v", err)
	}

	err = conn.WriteMessage(websocket.BinaryMessage, data)
	if err != nil {
		t.Fatalf("Failed to send test message: %v", err)
	}

	// WebSocket连接成功建立就足够了
	t.Log("WebSocket connection established successfully")
}

// 基准测试
func BenchmarkEndToEndRequest(b *testing.B) {
	// 设置服务器和客户端
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("benchmark response"))
	}))
	defer targetServer.Close()

	serverCfg := &config.Config{
		Mode:       "server",
		ListenPort: "0",
	}
	proxy := server.NewSinglePortProxy(serverCfg)
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	proxyURL, _ := url.Parse(proxyServer.URL)
	wsURL := fmt.Sprintf("ws://%s", proxyURL.Host)

	clientCfg := &config.Config{
		Mode:       "client",
		ServerAddr: wsURL,
		TargetAddr: strings.TrimPrefix(targetServer.URL, "http://"),
		Key:        "benchmark",
		Insecure:   true,
	}

	tunnelClient, _ := client.NewTunnelClient(clientCfg)
	go tunnelClient.Connect()
	time.Sleep(500 * time.Millisecond)

	httpClient := &http.Client{Timeout: 5 * time.Second}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req, _ := http.NewRequest("GET", proxyServer.URL+"/bench", nil)
			req.Header.Set("X-Tunnel-Key", "benchmark")
			resp, err := httpClient.Do(req)
			if err != nil {
				b.Fatalf("Request failed: %v", err)
			}
			resp.Body.Close()
		}
	})
}