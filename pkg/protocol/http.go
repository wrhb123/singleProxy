package protocol

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"singleproxy/pkg/logger"
)

// SerializeHTTPRequest 序列化HTTP请求
func SerializeHTTPRequest(r *http.Request) ([]byte, error) {
	logger := logger.WithFields(map[string]interface{}{
		"method":         r.Method,
		"url":            r.URL.String(),
		"content_length": r.ContentLength,
	})

	logger.Debug("Starting HTTP request serialization")

	var buf bytes.Buffer
	// 重建请求行
	reqURL := *r.URL
	reqURL.Scheme = "http"
	reqURL.Host = r.Host
	fmt.Fprintf(&buf, "%s %s HTTP/1.1\r\n", r.Method, reqURL.RequestURI())
	_ = r.Header.Write(&buf)
	buf.WriteString("\r\n")

	headerSize := buf.Len()

	if r.Body != nil {
		_, err := io.Copy(&buf, r.Body)
		if err != nil {
			logger.Error("Failed to copy request body during serialization",
				"error", err,
				"header_size", headerSize)
			return nil, err
		}
	}

	totalSize := buf.Len()

	logger.Debug("HTTP request serialization completed",
		"header_size", headerSize,
		"body_size", totalSize-headerSize,
		"total_size", totalSize)

	return buf.Bytes(), nil
}

// ParseHTTPRequest 解析HTTP请求
func ParseHTTPRequest(data []byte) (*http.Request, error) {
	logger.Debug("Starting HTTP request parsing",
		"data_size", len(data))

	reader := bufio.NewReader(bytes.NewReader(data))
	req, err := http.ReadRequest(reader)
	if err != nil {
		logger.Error("Failed to parse HTTP request",
			"data_size", len(data),
			"error", err)
		return nil, err
	}

	logger.Debug("HTTP request parsing completed",
		"method", req.Method,
		"url", req.URL.String(),
		"proto", req.Proto,
		"content_length", req.ContentLength,
		"header_count", len(req.Header))

	return req, nil
}

// DeserializeHTTPResponse 反序列化HTTP响应
func DeserializeHTTPResponse(data []byte) (*http.Response, error) {
	logger.Debug("Starting HTTP response deserialization",
		"data_size", len(data))
	
	reader := bufio.NewReader(bytes.NewReader(data))
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		logger.Error("Failed to deserialize HTTP response",
			"data_size", len(data),
			"error", err)
		return nil, err
	}
	
	logger.Debug("HTTP response deserialization completed",
		"status", resp.Status,
		"status_code", resp.StatusCode,
		"proto", resp.Proto,
		"content_length", resp.ContentLength,
		"header_count", len(resp.Header))
	
	return resp, nil
}
