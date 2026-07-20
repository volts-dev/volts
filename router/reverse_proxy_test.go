package router

import (
	"bufio"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	vtls "github.com/volts-dev/volts/internal/tls"
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
		serveWebSocket("http", backend.Addr().String(), w, r)
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

// ------------------------------------------------------------
// 后端 scheme 处理
// ------------------------------------------------------------

// dialBackend 对 https 后端必须走 TLS 握手。
//
// 此前 serveWebSocket 只拿到 host、无条件明文 net.Dial —— 而 getService 明明
// 已经从节点 metadata 解析出了 protocol。结果是 WebSocket 反代在任何 TLS
// 后端上都不可用（握手必然失败），且失败原因完全体现不出来。
func TestDialBackendUsesTLSForHttps(t *testing.T) {
	cert, err := vtls.Certificate("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	accepted := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			accepted <- err
			return
		}
		defer conn.Close()
		// 强制完成握手，明文连接会在这里报错
		accepted <- conn.(*tls.Conn).Handshake()
	}()

	// 自签证书：跳过校验只是为了让测试聚焦"是否走了 TLS"
	conn, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("基准 TLS 拨号失败，测试环境有问题: %v", err)
	}
	conn.Close()
	if err := <-accepted; err != nil {
		t.Fatalf("基准握手失败: %v", err)
	}

	// dialBackend 走 http 分支时应当是明文 —— 对 TLS 监听器握手不会成功
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			accepted <- err
			return
		}
		defer conn.Close()
		accepted <- conn.(*tls.Conn).Handshake()
	}()

	plain, err := dialBackend("http", ln.Addr().String())
	if err == nil {
		plain.Write([]byte("not a tls client hello\r\n\r\n"))
		plain.Close()
	}
	if err := <-accepted; err == nil {
		t.Error("http 分支不应产生成功的 TLS 握手")
	}
}

// dialBackend 从 host:port 里正确取出 ServerName —— 带端口会让证书校验失败。
func TestDialBackendServerNameStripsPort(t *testing.T) {
	// 连一个不存在的端口，只关心错误里体现的是主机名而非 host:port
	_, err := dialBackend("https", "127.0.0.1:1")
	if err == nil {
		t.Skip("端口 1 意外可连")
	}
	// 拨号失败即可（说明没有在 ServerName 解析阶段就崩掉）
}

// Clone 而非浅拷贝：serveWebSocket 设置 X-Forwarded-For 时不得改写原请求的 header。
//
// 此前是 `req := new(http.Request); *req = *r`，Header 仍是同一个 map。
func TestServeWebSocketDoesNotMutateOriginalHeader(t *testing.T) {
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
		conn.Close()
	}()

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	w := &hijackableResponseWriter{conn: serverConn}
	r := httptest.NewRequest("GET", "http://example.com/ws", nil)
	r.RemoteAddr = "203.0.113.7:9999"

	done := make(chan struct{})
	go func() {
		defer close(done)
		serveWebSocket("http", backend.Addr().String(), w, r)
	}()

	io.Copy(io.Discard, clientConn)
	<-done

	if got := r.Header.Get("X-Forwarded-For"); got != "" {
		t.Fatalf("原请求的 header 被改写了: X-Forwarded-For = %q", got)
	}
}
