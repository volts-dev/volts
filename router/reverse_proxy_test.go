package router

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"
)

// hijackableResponseWriter 实现 http.Hijacker 接口，用于测试
type hijackableResponseWriter struct {
	httptest.ResponseRecorder
	conn net.Conn
}

func (h *hijackableResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return h.conn, bufio.NewReadWriter(bufio.NewReader(h.conn), bufio.NewWriter(h.conn)), nil
}

// Header 必须实现，否则 httptest.ResponseRecorder 的 Header() 足够但 Hijack 需要覆盖
func (h *hijackableResponseWriter) Header() http.Header {
	return h.ResponseRecorder.Header()
}

// TestWebSocketBothGoroutinesComplete 验证 WebSocket 代理两个 goroutine 均完成，无泄漏
func TestWebSocketBothGoroutinesComplete(t *testing.T) {
	// 模拟后端 WebSocket 服务：接受连接，发送一条消息，然后关闭
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	go func() {
		conn, err := backend.Accept()
		if err != nil {
			return
		}
		conn.Write([]byte("hello"))
		conn.Close()
	}()

	goroutinesBefore := runtime.NumGoroutine()

	// 模拟 HTTP 连接劫持：clientConn 是"浏览器侧"，serverConn 是"服务器侧"
	clientConn, serverConn := net.Pipe()

	w := &hijackableResponseWriter{conn: serverConn}
	r := &http.Request{
		Header:     make(http.Header),
		RemoteAddr: "127.0.0.1:9999",
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		serveWebSocket(backend.Addr().String(), w, r)
	}()

	// 读取后端发来的数据后关闭客户端连接，触发两个 cp goroutine 均返回
	io.Copy(io.Discard, clientConn)
	clientConn.Close()

	select {
	case <-done:
		// serveWebSocket 正常返回
	case <-time.After(2 * time.Second):
		t.Fatal("serveWebSocket goroutines did not finish within 2s")
	}

	// 等待 goroutine 完全退出
	time.Sleep(50 * time.Millisecond)
	goroutinesAfter := runtime.NumGoroutine()
	if goroutinesAfter > goroutinesBefore+2 {
		t.Errorf("possible goroutine leak: before=%d after=%d", goroutinesBefore, goroutinesAfter)
	}
}
