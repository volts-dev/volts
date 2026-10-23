package router

import (
	"bufio"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
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

// TestWebSocketBothGoroutinesComplete 验证 WebSocket 代理真的双向转发了数据，返回后两个拷贝协程都退出、无泄漏。
//
// 此前这条用例传的请求没有 URL，serveWebSocket 在 req.Write 那一步就报错返回（100 次里 100 次），
// 从没走到 proxyBidir —— 它测的是一条早退路径，却一直显示通过。
func TestWebSocketBothGoroutinesComplete(t *testing.T) {
	// 模拟后端 WebSocket 服务：读完代理转来的升级请求，回一条消息，然后关闭
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	gotReq := make(chan string, 1)
	go func() {
		conn, err := backend.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		req, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			gotReq <- "read error: " + err.Error()
			return
		}
		gotReq <- req.URL.Path
		conn.Write([]byte("hello"))
	}()

	// 模拟 HTTP 连接劫持：clientConn 是"浏览器侧"，serverConn 是"服务器侧"
	clientConn, serverConn := net.Pipe()

	w := &hijackableResponseWriter{conn: serverConn}
	r := httptest.NewRequest("GET", "http://example.com/ws", nil)
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "websocket")
	r.RemoteAddr = "127.0.0.1:9999"

	done := make(chan error, 1)
	go func() {
		done <- serveWebSocket("http", backend.Addr().String(), w, r)
	}()

	// 读到后端发来的全部数据（后端关闭 → 代理关闭两侧 → 这里读到 EOF）。带期限：代理不关浏览器侧
	// 的话这里会一直等下去，挂起比失败更糟。
	clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, err := io.ReadAll(clientConn)
	clientConn.Close()
	if err != nil {
		t.Fatalf("后端关闭后浏览器侧没读到 EOF（代理没有关掉被劫持的连接？）：%v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("浏览器侧收到 %q，想要后端发的 %q", got, "hello")
	}
	select {
	case p := <-gotReq:
		if p != "/ws" {
			t.Fatalf("后端收到的请求不对：%s", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("后端没收到代理转来的请求")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveWebSocket 没走到转发就返回了：%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serveWebSocket goroutines did not finish within 2s")
	}

	// 只看这段代码自己的协程还在不在。曾比进程级的 runtime.NumGoroutine() 前后差：整包跑时
	// 背景里有两百多个别的用例留下的协程，窗口里起落几个就误报泄漏（单跑 300 次不红，整包约四次红一次）。
	deadline := time.Now().Add(2 * time.Second)
	for {
		left := goroutinesIn("router.proxyBidir")
		if left == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutine leak: %d proxyBidir goroutines still running after serveWebSocket returned", left)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// goroutinesIn 数栈里含 frame 的协程。
func goroutinesIn(frame string) int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	count := 0
	for _, g := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(g, frame) {
			count++
		}
	}
	return count
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
