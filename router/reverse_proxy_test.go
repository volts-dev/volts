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

// TestProxyBidir_OneSideClosedUnblocksOther 验证：
// proxyBidir 在一侧断开后必须主动 Close 两侧，让另一侧阻塞的 Copy 立即返回。
// 修复前：bidir 等两次 errCh，但第二个 Copy 还在 Read 一个仍然活着的 pipe，永久死锁。
// 修复后：首个 err 到达后立即 Close 双端，强制对方 Read 返回，bidir 在合理时间内退出。
func TestProxyBidir_OneSideClosedUnblocksOther(t *testing.T) {
	// 模拟"后端"：a <-> aPeer。aPeer 给 proxyBidir 用。
	a, aPeer := net.Pipe()
	// 模拟"浏览器"：b <-> bPeer。bPeer 给 proxyBidir 用。
	b, bPeer := net.Pipe()

	// 浏览器侧（b）：永远不发也不读
	defer b.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		proxyBidir(aPeer, bPeer)
	}()

	// 关闭后端侧 a —— aPeer 收到 EOF，一侧 Copy 立即返回。
	// 若 proxyBidir 没有 Close 对端，bPeer.Read 会永久阻塞。
	a.Close()

	select {
	case <-done:
		// 修复后：另一侧也被 Close，Copy 解除阻塞
	case <-time.After(2 * time.Second):
		t.Fatal("proxyBidir deadlocked: closed one side but other Copy still blocked")
	}
}
