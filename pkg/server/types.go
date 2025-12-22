package server

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"sync"
)

// prefixedConn 包装连接以支持回放读取的前缀数据
type prefixedConn struct {
	net.Conn
	prefix     []byte
	prefixRead bool
}

func (pc *prefixedConn) Read(b []byte) (int, error) {
	if !pc.prefixRead && len(pc.prefix) > 0 {
		n := copy(b, pc.prefix)
		if n < len(pc.prefix) {
			pc.prefix = pc.prefix[n:]
		} else {
			pc.prefixRead = true
		}
		return n, nil
	}
	return pc.Conn.Read(b)
}

// singleConnListener 实现net.Listener接口，只提供一个连接
type singleConnListener struct {
	conn net.Conn
	used bool
	mu   sync.Mutex
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.used {
		return nil, fmt.Errorf("connection already used")
	}
	l.used = true
	return l.conn, nil
}

func (l *singleConnListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.conn != nil {
		return l.conn.Close()
	}
	return nil
}

func (l *singleConnListener) Addr() net.Addr {
	if l.conn != nil {
		return l.conn.LocalAddr()
	}
	return nil
}

// httpResponseWriter 实现http.ResponseWriter接口
type httpResponseWriter struct {
	conn          net.Conn
	header        http.Header
	statusCode    int
	headerWritten bool
	hijacked      bool
}

func (w *httpResponseWriter) Header() http.Header {
	return w.header
}

func (w *httpResponseWriter) WriteHeader(statusCode int) {
	if w.headerWritten {
		return
	}
	w.statusCode = statusCode
	w.headerWritten = true

	// 写入状态行
	fmt.Fprintf(w.conn, "HTTP/1.1 %d %s\r\n", statusCode, http.StatusText(statusCode))

	// 写入头部
	for key, values := range w.header {
		for _, value := range values {
			fmt.Fprintf(w.conn, "%s: %s\r\n", key, value)
		}
	}
	fmt.Fprintf(w.conn, "\r\n")
}

func (w *httpResponseWriter) Write(data []byte) (int, error) {
	if !w.headerWritten {
		w.WriteHeader(http.StatusOK)
	}
	return w.conn.Write(data)
}

// Hijacker 接口实现，用于WebSocket升级
func (w *httpResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.hijacked {
		return nil, nil, fmt.Errorf("connection already hijacked")
	}
	w.hijacked = true
	return w.conn, bufio.NewReadWriter(bufio.NewReader(w.conn), bufio.NewWriter(w.conn)), nil
}

// Flusher 接口实现，用于流式传输
func (w *httpResponseWriter) Flush() {
	// 对于TCP连接，数据会立即发送
	// 这里我们可以添加一个空实现，因为底层的TCP连接会处理刷新
}