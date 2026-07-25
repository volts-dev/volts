package router

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/volts-dev/volts/transport"
)

// wsHandshakeRequest 构造一个合法的 WebSocket 升级请求。
func wsHandshakeRequest(path string) *http.Request {
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Sec-WebSocket-Version", "13")
	r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	return r
}

// serveWs 在测试 router 上注册一条升级路由并驱动一次 ServeHTTP。
// 返回浏览器侧连接，测试从它读回握手响应与后续帧。
func serveWs(t *testing.T, r *TRouter, path string, req *http.Request,
	handler func(ctx *THttpContext)) (net.Conn, *hijackableResponseWriter, *sync.WaitGroup) {
	t.Helper()

	grp := NewGroup()
	grp.Url([]string{"GET"}, path, handler)
	r.RegisterGroup(grp)

	clientConn, serverConn := net.Pipe()
	clientConn.SetDeadline(time.Now().Add(5 * time.Second))
	serverConn.SetDeadline(time.Now().Add(5 * time.Second))
	t.Cleanup(func() {
		clientConn.Close()
		serverConn.Close()
	})

	// 必须用 NewRecorder 初始化：零值 ResponseRecorder 的 Body 是 nil，
	// 其 Write 会静默丢弃全部字节，任何"有没有写出来"的断言都将恒绿。
	w := &hijackableResponseWriter{conn: serverConn}
	w.ResponseRecorder = *httptest.NewRecorder()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.ServeHTTP(w, transport.NewHttpRequest(req))
	}()
	return clientConn, w, &wg
}

func TestWebsocketUpgrade_HandshakeSuccess(t *testing.T) {
	r := New()
	defer close(r.exit)

	done := make(chan struct{})
	client, _, _ := serveWs(t, r, "/ws", wsHandshakeRequest("/ws"), func(ctx *THttpContext) {
		ws, err := ctx.Upgrade()
		if err != nil {
			t.Errorf("Upgrade 失败: %v", err)
			close(done)
			return
		}
		go func() {
			defer close(done)
			defer ws.Close()
			ws.WriteMessage(transport.WsOpText, []byte("hi"))
		}()
	})

	br := bufio.NewReader(client)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("读握手响应失败: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("状态码 = %d，期望 101", resp.StatusCode)
	}
	if !strings.EqualFold(resp.Header.Get("Upgrade"), "websocket") {
		t.Errorf("Upgrade 头 = %q", resp.Header.Get("Upgrade"))
	}

	// RFC 6455 §4.2.2：accept = base64(sha1(key + GUID))，客户端会据此校验。
	h := sha1.New()
	h.Write([]byte("dGhlIHNhbXBsZSBub25jZQ=="))
	h.Write([]byte(wsAcceptGUID))
	want := base64.StdEncoding.EncodeToString(h.Sum(nil))
	if got := resp.Header.Get("Sec-WebSocket-Accept"); got != want {
		t.Errorf("Sec-WebSocket-Accept = %q，期望 %q", got, want)
	}

	// 握手之后紧接着应当是服务端发的文本帧，中间不得夹带任何字节。
	cli := transport.NewWebsocketConn(client, br, false, "")
	op, data, err := cli.ReadMessage()
	if err != nil {
		t.Fatalf("读业务帧失败: %v", err)
	}
	if op != transport.WsOpText || string(data) != "hi" {
		t.Errorf("业务帧 = (%d,%q)，期望 (1,\"hi\")", op, data)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("超时：处理 goroutine 未结束")
	}
}

// 劫持成功后 router 绝不能再走 Apply()。
//
// 注意失败形态：net/http 的 ResponseWriter 在 Hijack 之后写入会返回 ErrHijacked，
// 所以 Apply 并不会真的把字节打进已升级的连接。它的实际危害是照常走完
// “提交响应 + 归还池”的收尾流程 —— 断言落在 Apply 是否被调用上，
// 用 ResponseWriter 有没有被写过来观测。
func TestWebsocketUpgrade_RouterDoesNotApplyAfterHijack(t *testing.T) {
	r := New()
	defer close(r.exit)

	ready := make(chan struct{})
	client, w, wg := serveWs(t, r, "/ws-clean", wsHandshakeRequest("/ws-clean"), func(ctx *THttpContext) {
		// 留下待提交的结果：Apply 一旦被调用就会把它写进 ResponseWriter。
		ctx.Result = []byte("THIS-MUST-NOT-BE-WRITTEN")
		ws, err := ctx.Upgrade()
		if err != nil {
			t.Errorf("Upgrade 失败: %v", err)
			close(ready)
			return
		}
		go func() {
			defer close(ready)
			ws.WriteMessage(transport.WsOpBinary, []byte{0xAA})
		}()
	})

	br := bufio.NewReader(client)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("读握手响应失败: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("状态码 = %d，期望 101", resp.StatusCode)
	}

	// 必须先读再等：net.Pipe 无缓冲，写方阻塞在等读，
	// 反过来先 <-ready 就是测试自己制造的死锁。
	cli := transport.NewWebsocketConn(client, br, false, "")
	op, data, err := cli.ReadMessage()
	if err != nil {
		t.Fatalf("101 之后的第一帧解析失败（很可能被 Apply 的输出污染了）: %v", err)
	}
	if op != transport.WsOpBinary || len(data) != 1 || data[0] != 0xAA {
		t.Errorf("第一帧 = (%d,% x)，期望 (2,AA)", op, data)
	}

	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("超时：处理 goroutine 未结束")
	}

	// 必须等 ServeHTTP 收尾：Apply 发生在处理器返回之后，
	// 不等就会在它还没跑到的时候断言"没写"，测试恒绿。
	wg.Wait()

	// Apply 若被调用，sentinel 会落进 ResponseWriter。
	if body := w.ResponseRecorder.Body.String(); strings.Contains(body, "THIS-MUST-NOT-BE-WRITTEN") {
		t.Errorf("劫持后仍调用了 Apply()，写出了 %q", body)
	}
}

// 劫持后的 context 不得回到池里：WS goroutine 还持有它，
// 复用会让两个请求同时读写同一个 THttpContext。
func TestWebsocketUpgrade_ContextNotReturnedToPool(t *testing.T) {
	r := New()
	defer close(r.exit)

	var hijackedCtx *THttpContext
	done := make(chan struct{})
	client, _, wg := serveWs(t, r, "/ws-pool", wsHandshakeRequest("/ws-pool"), func(ctx *THttpContext) {
		hijackedCtx = ctx
		ws, err := ctx.Upgrade()
		if err != nil {
			t.Errorf("Upgrade 失败: %v", err)
			close(done)
			return
		}
		go func() {
			defer close(done)
			ws.WriteMessage(transport.WsOpText, []byte("x"))
		}()
	})

	br := bufio.NewReader(client)
	if _, err := http.ReadResponse(br, nil); err != nil {
		t.Fatalf("读握手响应失败: %v", err)
	}
	// 同样先把帧读走，别让写方吊死在无缓冲管道上。
	cli := transport.NewWebsocketConn(client, br, false, "")
	if _, _, err := cli.ReadMessage(); err != nil {
		t.Fatalf("读业务帧失败: %v", err)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("超时：处理 goroutine 未结束")
	}
	wg.Wait()

	if hijackedCtx == nil {
		t.Fatal("处理器没跑到")
	}
	if !hijackedCtx.Hijacked() {
		t.Error("Hijacked() 应为 true")
	}
	// response 仍须挂在 ctx 上：归还池的路径会把 ResponseWriter 置 nil，
	// 那正是 WS goroutine 拿到 nil 的时刻。
	if hijackedCtx.Response() == nil || hijackedCtx.Response().ResponseWriter == nil {
		t.Error("劫持后 response 被清空了，说明走了池回收路径")
	}

	route, _ := r.tree.Match("GET", "/ws-pool")
	if route == nil {
		t.Fatal("路由丢失")
	}
	p, ok := r.httpCtxPool.Load(route.Id())
	if !ok {
		t.Fatal("池不存在")
	}
	got := p.(*sync.Pool).Get().(*THttpContext)
	if got == hijackedCtx {
		t.Error("被劫持的 context 回到了池里，会被下一个请求复用（use-after-free）")
	}
}

// reset 必须清掉上一轮的 hijacked 标记，否则复用到的 context 会让
// router 对一个普通请求也跳过 Apply —— 表现为接口恒返回空响应体。
func TestWebsocketUpgrade_ResetClearsHijackedFlag(t *testing.T) {
	ctx := NewHttpContext(New())
	ctx.hijacked = true

	req := transport.NewHttpRequest(httptest.NewRequest("GET", "/x", nil))
	rsp := transport.NewHttpResponse(req.Context(), req)
	ctx.reset(rsp, req)

	if ctx.Hijacked() {
		t.Error("reset 后 hijacked 仍为 true，池复用会污染下一个请求")
	}
}

// 握手校验的各条失败路径都应返回明确错误且不劫持连接。
func TestWebsocketUpgrade_HandshakeValidation(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(r *http.Request)
		wantErr error
	}{
		{"非 GET", func(r *http.Request) { r.Method = "POST" }, ErrWsNotUpgrade},
		{"缺 Connection", func(r *http.Request) { r.Header.Del("Connection") }, ErrWsNotUpgrade},
		{"缺 Upgrade", func(r *http.Request) { r.Header.Del("Upgrade") }, ErrWsNotUpgrade},
		{"版本不是 13", func(r *http.Request) { r.Header.Set("Sec-WebSocket-Version", "8") }, ErrWsBadVersion},
		{"Key 非法 base64", func(r *http.Request) { r.Header.Set("Sec-WebSocket-Key", "!!!") }, ErrWsBadKey},
		{"Key 长度不是 16 字节", func(r *http.Request) { r.Header.Set("Sec-WebSocket-Key", "c2hvcnQ=") }, ErrWsBadKey},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := wsHandshakeRequest("/x")
			c.mutate(req)

			ctx := NewHttpContext(New())
			rsp := transport.NewHttpResponse(req.Context(), transport.NewHttpRequest(req))
			rsp.Connect(httptest.NewRecorder())
			ctx.reset(rsp, transport.NewHttpRequest(req))

			up := &TWebsocketUpgrader{}
			_, err := up.Upgrade(ctx)
			if err == nil {
				t.Fatalf("%s 应当握手失败", c.name)
			}
			if !strings.Contains(err.Error(), strings.SplitN(c.wantErr.Error(), ":", 2)[1]) &&
				err != c.wantErr {
				t.Errorf("err = %v，期望包含 %v", err, c.wantErr)
			}
			if ctx.Hijacked() {
				t.Error("握手失败却标记了劫持")
			}
		})
	}
}

// 默认策略是同源。跨站 Origin 必须被拒 —— 浏览器不给 WebSocket 做同源限制，
// 放行等于把端点交给任意第三方页面（CSWSH）。
func TestWebsocketUpgrade_DefaultRejectsCrossOrigin(t *testing.T) {
	req := wsHandshakeRequest("/x")
	req.Host = "app.example.com"
	req.Header.Set("Origin", "https://evil.example.net")

	ctx := NewHttpContext(New())
	rsp := transport.NewHttpResponse(req.Context(), transport.NewHttpRequest(req))
	rsp.Connect(httptest.NewRecorder())
	ctx.reset(rsp, transport.NewHttpRequest(req))

	up := &TWebsocketUpgrader{}
	if _, err := up.Upgrade(ctx); err != ErrWsOriginNotAllowed {
		t.Errorf("跨站 Origin err = %v，期望 ErrWsOriginNotAllowed", err)
	}
}

func TestCheckSameOrigin(t *testing.T) {
	cases := []struct {
		origin string
		host   string
		want   bool
	}{
		{"", "app.example.com", true}, // 非浏览器客户端不发 Origin
		{"https://app.example.com", "app.example.com", true},
		{"http://app.example.com", "app.example.com", true},
		{"https://APP.example.com", "app.example.com", true}, // 主机名大小写不敏感
		{"https://evil.example.net", "app.example.com", false},
		{"https://app.example.com.evil.net", "app.example.com", false},
		{"://bad", "app.example.com", false},
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", "/", nil)
		req.Host = c.host
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		if got := checkSameOrigin(transport.NewHttpRequest(req)); got != c.want {
			t.Errorf("origin=%q host=%q: 得 %v，期望 %v", c.origin, c.host, got, c.want)
		}
	}
}

func TestWebsocketUpgrade_SubprotocolNegotiation(t *testing.T) {
	cases := []struct {
		name   string
		server []string
		client string
		want   string
	}{
		{"无交集", []string{"a"}, "b, c", ""},
		{"取服务端优先序", []string{"a", "b"}, "b, a", "a"},
		{"单个命中", []string{"volts.bus"}, "volts.bus", "volts.bus"},
		{"服务端未声明", nil, "anything", ""},
		{"客户端未声明", []string{"a"}, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := wsHandshakeRequest("/x")
			if c.client != "" {
				req.Header.Set("Sec-WebSocket-Protocol", c.client)
			}
			up := &TWebsocketUpgrader{Subprotocols: c.server}
			if got := up.selectSubprotocol(transport.NewHttpRequest(req)); got != c.want {
				t.Errorf("协商结果 = %q，期望 %q", got, c.want)
			}
		})
	}
}

// Connection 头是逗号分隔的 token 列表，必须按 token 比较。
// 用 strings.Contains 会把 `Connection: keep-alive` 误判成含 upgrade。
func TestHeaderContainsToken(t *testing.T) {
	cases := []struct {
		header string
		value  string
		token  string
		want   bool
	}{
		{"Connection", "Upgrade", "upgrade", true},
		{"Connection", "keep-alive, Upgrade", "upgrade", true},
		{"Connection", "keep-alive", "upgrade", false},
		{"Connection", "upgrades", "upgrade", false},
		{"Upgrade", "websocket", "websocket", true},
		{"Upgrade", "WebSocket", "websocket", true},
		{"Upgrade", "websocketx", "websocket", false},
		{"Upgrade", "h2c, websocket", "websocket", true},
	}
	for _, c := range cases {
		h := http.Header{}
		h.Set(c.header, c.value)
		if got := headerContainsToken(h, c.header, c.token); got != c.want {
			t.Errorf("%s: %q 含 %q = %v，期望 %v", c.header, c.value, c.token, got, c.want)
		}
	}
}

func TestIsWebsocket(t *testing.T) {
	ctx := NewHttpContext(New())

	ws := transport.NewHttpRequest(wsHandshakeRequest("/x"))
	rsp := transport.NewHttpResponse(ws.Context(), ws)
	rsp.Connect(httptest.NewRecorder())
	ctx.reset(rsp, ws)
	if !ctx.IsWebsocket() {
		t.Error("升级请求应被识别为 WebSocket")
	}

	plain := transport.NewHttpRequest(httptest.NewRequest("GET", "/x", nil))
	ctx.reset(rsp, plain)
	if ctx.IsWebsocket() {
		t.Error("普通请求被误判为 WebSocket")
	}
}
