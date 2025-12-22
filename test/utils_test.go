package test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"singleproxy/pkg/utils"
)

func TestForwardToTarget(t *testing.T) {
	// 创建目标服务器
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"message": "Hello from target"}`))
	}))
	defer targetServer.Close()

	// 获取目标服务器地址（去掉 http:// 前缀）
	targetAddr := strings.TrimPrefix(targetServer.URL, "http://")

	// 创建测试请求
	req := httptest.NewRequest("GET", "http://proxy.example.com/api/test", nil)
	req.Header.Set("User-Agent", "Test-Client")
	req.Header.Set("Connection", "keep-alive") // 这个头部应该被移除
	req.Header.Set("Proxy-Authorization", "Bearer token") // 这个头部也应该被移除

	// 转发请求
	resp, err := utils.ForwardToTarget(req, targetAddr)
	if err != nil {
		t.Fatalf("Failed to forward request: %v", err)
	}
	defer resp.Body.Close()

	// 验证响应
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, resp.StatusCode)
	}

	if contentType := resp.Header.Get("Content-Type"); contentType != "application/json" {
		t.Errorf("Expected Content-Type 'application/json', got %s", contentType)
	}

	// 验证请求的URL被正确修改
	if req.URL.Scheme != "http" {
		t.Errorf("Expected URL scheme 'http', got %s", req.URL.Scheme)
	}

	if req.URL.Host != targetAddr {
		t.Errorf("Expected URL host '%s', got %s", targetAddr, req.URL.Host)
	}

	// 验证代理相关头部被移除
	if req.Header.Get("Connection") != "" {
		t.Error("Connection header should be removed")
	}

	if req.Header.Get("Proxy-Authorization") != "" {
		t.Error("Proxy-Authorization header should be removed")
	}

	// 验证其他头部保留
	if req.Header.Get("User-Agent") != "Test-Client" {
		t.Error("User-Agent header should be preserved")
	}
}

func TestForwardToTarget_InvalidTarget(t *testing.T) {
	// 测试无效目标地址
	req := httptest.NewRequest("GET", "http://example.com/test", nil)
	
	_, err := utils.ForwardToTarget(req, "nonexistent.invalid:9999")
	if err == nil {
		t.Error("Expected error for invalid target address")
	}
}

func TestForwardToTarget_Timeout(t *testing.T) {
	// 创建一个慢响应的服务器
	slowServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(35 * time.Second) // 超过30秒超时
		w.WriteHeader(http.StatusOK)
	}))
	defer slowServer.Close()

	targetAddr := strings.TrimPrefix(slowServer.URL, "http://")
	req := httptest.NewRequest("GET", "http://example.com/test", nil)

	// 这应该超时
	_, err := utils.ForwardToTarget(req, targetAddr)
	if err == nil {
		t.Error("Expected timeout error")
	}
}

func TestGetClientIP_XForwardedFor(t *testing.T) {
	req := httptest.NewRequest("GET", "http://example.com/test", nil)
	req.Header.Set("X-Forwarded-For", "192.168.1.100")
	req.RemoteAddr = "10.0.0.1:12345"

	ip, err := utils.GetClientIP(req)
	if err != nil {
		t.Fatalf("Failed to get client IP: %v", err)
	}

	if ip != "192.168.1.100" {
		t.Errorf("Expected IP '192.168.1.100', got '%s'", ip)
	}
}

func TestGetClientIP_XRealIP(t *testing.T) {
	req := httptest.NewRequest("GET", "http://example.com/test", nil)
	req.Header.Set("X-Real-IP", "203.0.113.42")
	req.RemoteAddr = "10.0.0.1:12345"

	ip, err := utils.GetClientIP(req)
	if err != nil {
		t.Fatalf("Failed to get client IP: %v", err)
	}

	if ip != "203.0.113.42" {
		t.Errorf("Expected IP '203.0.113.42', got '%s'", ip)
	}
}

func TestGetClientIP_RemoteAddr(t *testing.T) {
	req := httptest.NewRequest("GET", "http://example.com/test", nil)
	req.RemoteAddr = "172.16.0.50:54321"

	ip, err := utils.GetClientIP(req)
	if err != nil {
		t.Fatalf("Failed to get client IP: %v", err)
	}

	if ip != "172.16.0.50" {
		t.Errorf("Expected IP '172.16.0.50', got '%s'", ip)
	}
}

func TestGetClientIP_Priority(t *testing.T) {
	// 测试头部优先级：X-Forwarded-For > X-Real-IP > RemoteAddr
	req := httptest.NewRequest("GET", "http://example.com/test", nil)
	req.Header.Set("X-Forwarded-For", "1.1.1.1")
	req.Header.Set("X-Real-IP", "2.2.2.2")
	req.RemoteAddr = "3.3.3.3:12345"

	ip, err := utils.GetClientIP(req)
	if err != nil {
		t.Fatalf("Failed to get client IP: %v", err)
	}

	if ip != "1.1.1.1" {
		t.Errorf("Expected X-Forwarded-For IP '1.1.1.1' to take priority, got '%s'", ip)
	}
}

func TestGetClientIP_InvalidRemoteAddr(t *testing.T) {
	req := httptest.NewRequest("GET", "http://example.com/test", nil)
	req.RemoteAddr = "invalid-addr" // 无效的地址格式

	_, err := utils.GetClientIP(req)
	if err == nil {
		t.Error("Expected error for invalid remote address")
	}
}

func TestMin(t *testing.T) {
	tests := []struct {
		name     string
		a, b     int
		expected int
	}{
		{"a smaller", 3, 5, 3},
		{"b smaller", 10, 7, 7},
		{"equal", 4, 4, 4},
		{"negative numbers", -5, -2, -5},
		{"zero and positive", 0, 3, 0},
		{"negative and positive", -1, 1, -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := utils.Min(tt.a, tt.b)
			if result != tt.expected {
				t.Errorf("Min(%d, %d) = %d, expected %d", tt.a, tt.b, result, tt.expected)
			}
		})
	}
}

func TestForwardToTarget_MethodPreservation(t *testing.T) {
	// 测试不同HTTP方法是否被正确保留
	methods := []string{"GET", "POST", "PUT", "DELETE", "PATCH"}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			// 创建目标服务器
			targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != method {
					t.Errorf("Expected method %s, got %s", method, r.Method)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer targetServer.Close()

			targetAddr := strings.TrimPrefix(targetServer.URL, "http://")
			req := httptest.NewRequest(method, "http://example.com/test", nil)

			resp, err := utils.ForwardToTarget(req, targetAddr)
			if err != nil {
				t.Fatalf("Failed to forward %s request: %v", method, err)
			}
			resp.Body.Close()
		})
	}
}

func TestForwardToTarget_HeaderCleaning(t *testing.T) {
	// 测试所有应该被移除的头部
	headersToRemove := []string{
		"Connection",
		"Keep-Alive", 
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"TE",
		"Trailers",
		"Transfer-Encoding",
		"Upgrade",
	}

	// 创建目标服务器
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证这些头部都被移除了
		for _, header := range headersToRemove {
			if r.Header.Get(header) != "" {
				t.Errorf("Header %s should have been removed", header)
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer targetServer.Close()

	targetAddr := strings.TrimPrefix(targetServer.URL, "http://")
	req := httptest.NewRequest("GET", "http://example.com/test", nil)

	// 设置所有应该被移除的头部
	for _, header := range headersToRemove {
		req.Header.Set(header, "test-value")
	}

	// 设置一些应该保留的头部
	req.Header.Set("User-Agent", "test-agent")
	req.Header.Set("Accept", "application/json")

	resp, err := utils.ForwardToTarget(req, targetAddr)
	if err != nil {
		t.Fatalf("Failed to forward request: %v", err)
	}
	defer resp.Body.Close()

	// 验证应该保留的头部还在
	if req.Header.Get("User-Agent") != "test-agent" {
		t.Error("User-Agent header should be preserved")
	}
	if req.Header.Get("Accept") != "application/json" {
		t.Error("Accept header should be preserved")
	}
}

// 基准测试
func BenchmarkGetClientIP_XForwardedFor(b *testing.B) {
	req := httptest.NewRequest("GET", "http://example.com/test", nil)
	req.Header.Set("X-Forwarded-For", "192.168.1.100")
	req.RemoteAddr = "10.0.0.1:12345"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = utils.GetClientIP(req)
	}
}

func BenchmarkGetClientIP_RemoteAddr(b *testing.B) {
	req := httptest.NewRequest("GET", "http://example.com/test", nil)
	req.RemoteAddr = "172.16.0.50:54321"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = utils.GetClientIP(req)
	}
}

func BenchmarkMin(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = utils.Min(i, i+1)
	}
}