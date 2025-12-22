package utils

import (
	"fmt"
	"net"
	"net/http"
	"singleproxy/pkg/logger"
	"strings"
	"time"
)

// ForwardToTarget 转发请求到目标服务器
func ForwardToTarget(req *http.Request, targetAddr string) (*http.Response, error) {
	originalURL := req.URL.String()
	startTime := time.Now()

	logger.Debug("Starting request forwarding to target",
		"original_url", originalURL,
		"target_addr", targetAddr,
		"method", req.Method,
		"content_length", req.ContentLength,
		"user_agent", req.Header.Get("User-Agent"))

	req.URL.Scheme = "http"
	req.URL.Host = targetAddr
	req.RequestURI = ""

	newURL := req.URL.String()
	logger.Debug("Modified request URL for forwarding",
		"original_url", originalURL,
		"target_url", newURL,
		"target_addr", targetAddr)

	// 清除代理相关的头部
	headersToRemove := []string{
		"Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "TE", "Trailers",
		"Transfer-Encoding", "Upgrade",
	}

	removedCount := 0
	for _, header := range headersToRemove {
		if req.Header.Get(header) != "" {
			req.Header.Del(header)
			removedCount++
		}
	}

	logger.Debug("Cleaned proxy-related headers",
		"target_addr", targetAddr,
		"headers_removed", removedCount,
		"remaining_headers", len(req.Header))

	client := &http.Client{Timeout: 30 * time.Second}

	logger.Debug("Sending request to target",
		"target_url", newURL,
		"method", req.Method,
		"timeout", "30s")

	resp, err := client.Do(req)
	duration := time.Since(startTime)

	if err != nil {
		logger.Error("Failed to forward request to target",
			"target_url", newURL,
			"method", req.Method,
			"duration", duration,
			"error", err)
		return nil, err
	}

	logger.Debug("Successfully received response from target",
		"target_url", newURL,
		"method", req.Method,
		"status", resp.Status,
		"status_code", resp.StatusCode,
		"content_length", resp.ContentLength,
		"duration", duration,
		"response_headers", SanitizeHeaders(resp.Header))

	return resp, nil
}

// GetClientIP 获取客户端真实IP
func GetClientIP(r *http.Request) (string, error) {
	// 尝试从 X-Forwarded-For 获取
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return xff, nil
	}

	// 尝试从 X-Real-IP 获取
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri, nil
	}

	// 从 RemoteAddr 获取
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return "", fmt.Errorf("failed to parse remote address: %v", err)
	}

	return ip, nil
}

// Min 返回两个整数中较小的值
func Min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// SanitizeHeaders 清理HTTP头信息，移除敏感信息用于日志记录
func SanitizeHeaders(headers http.Header) map[string][]string {
	sanitized := make(map[string][]string)
	for k, v := range headers {
		key := strings.ToLower(k)
		// 过滤敏感头信息
		if key == "authorization" || key == "cookie" || key == "x-tunnel-key" {
			sanitized[k] = []string{"[REDACTED]"}
		} else {
			sanitized[k] = v
		}
	}
	return sanitized
}
