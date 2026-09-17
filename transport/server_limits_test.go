package transport

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestApplyServerLimits_DebugDoesNotOpenTheHeaderPhase 锁定本轮修复的核心：
// Debug() 把读写超时放到 999s 之后，**读请求头**与**空闲**两段仍然是有界的。
//
// 不设这两项时标准库回落到 ReadTimeout —— 而 vectors 的 cmd.Start() 是无条件调
// volts.Debug() 的（实测：读/写/拨号一起变成 16m39s），于是线上每一个进程都允许
// 一条连接花 16 分钟去发几百字节的请求头。那就是 Slowloris 要的全部条件。
func TestApplyServerLimits_DebugDoesNotOpenTheHeaderPhase(t *testing.T) {
	cfg := newConfig(Debug())
	if cfg.ReadTimeout < 900*time.Second {
		t.Fatalf("前提变了：Debug 不再放大 ReadTimeout（%v），本测试要重写", cfg.ReadTimeout)
	}

	srv := &http.Server{}
	applyServerLimits(srv, cfg)

	if srv.ReadTimeout != cfg.ReadTimeout || srv.WriteTimeout != cfg.WriteTimeout {
		t.Errorf("读写超时应原样沿用配置：read=%v write=%v", srv.ReadTimeout, srv.WriteTimeout)
	}
	if srv.ReadHeaderTimeout != DefaultReadHeaderTimeout {
		t.Errorf("ReadHeaderTimeout=%v，应为 %v —— 断点停在 handler 里，收请求头没有理由要几分钟",
			srv.ReadHeaderTimeout, DefaultReadHeaderTimeout)
	}
	if srv.IdleTimeout != DefaultIdleTimeout {
		t.Errorf("IdleTimeout=%v，应为 %v", srv.IdleTimeout, DefaultIdleTimeout)
	}
	if srv.MaxHeaderBytes != DefaultMaxHeaderBytes {
		t.Errorf("MaxHeaderBytes=%d，应为 %d", srv.MaxHeaderBytes, DefaultMaxHeaderBytes)
	}
}

// TestApplyServerLimits_ExplicitValuesWin 锁定显式配置优先，且 <=0 回落到安全默认
// 而不是"不限"（不限就会回落到 ReadTimeout，正是要避免的那件事）。
func TestApplyServerLimits_ExplicitValuesWin(t *testing.T) {
	cfg := newConfig(
		WithReadHeaderTimeout(3*time.Second),
		WithIdleTimeout(7*time.Second),
		WithMaxHeaderBytes(4096),
	)
	srv := &http.Server{}
	applyServerLimits(srv, cfg)
	if srv.ReadHeaderTimeout != 3*time.Second || srv.IdleTimeout != 7*time.Second || srv.MaxHeaderBytes != 4096 {
		t.Fatalf("显式配置没生效: %+v", srv)
	}

	zero := newConfig(WithReadHeaderTimeout(0), WithIdleTimeout(0), WithMaxHeaderBytes(0))
	srv2 := &http.Server{}
	applyServerLimits(srv2, zero)
	if srv2.ReadHeaderTimeout != DefaultReadHeaderTimeout || srv2.IdleTimeout != DefaultIdleTimeout ||
		srv2.MaxHeaderBytes != DefaultMaxHeaderBytes {
		t.Fatalf("<=0 应回落到安全默认，实际 %+v", srv2)
	}
}

// TestSlowHeaderConnectionIsClosed 是真连接上的验收：只发半行请求行、之后一个字节
// 都不发，服务端必须在 ReadHeaderTimeout 到点时把连接关掉。
//
// 这条不是断言字段值，而是**行为**：把 applyServerLimits 里 ReadHeaderTimeout 那几行
// 去掉，标准库回落到 ReadTimeout（这里故意设成很大），连接就会一直挂着，本测试超时。
func TestSlowHeaderConnectionIsClosed(t *testing.T) {
	cfg := newConfig(
		Timeout(30*time.Second),                     // 整体读写超时故意放大
		WithReadHeaderTimeout(300*time.Millisecond), // 只有这一段是短的
	)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	applyServerLimits(srv, cfg)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go srv.Serve(ln)
	defer srv.Close()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// 半个请求行，永远不发完 —— 经典的慢速头攻击形态。
	if _, err := conn.Write([]byte("GET / HTT")); err != nil {
		t.Fatal(err)
	}

	// 判据是**服务端多久放弃这条连接**，不是它放弃时说了什么：标准库在读头超时时
	// 会先回一句 400/408 再关（带 Connection: close），所以这里读到 EOF 为止。
	//
	// 给 5 秒余量，而回落时会是 30 秒的 ReadTimeout —— 两者差得足够开，不会因为
	// 机器慢而误判；去掉 ReadHeaderTimeout 的话这里会一直读不到 EOF 直到读超时。
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	start := time.Now()
	_, err = io.ReadAll(conn)
	elapsed := time.Since(start)

	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatalf("等了 %v 服务端仍未放弃这条只发了半行请求头的连接 —— 慢速头攻击可以一直占着它", elapsed)
	}
	if err != nil {
		t.Fatalf("读连接出错: %v", err)
	}
	if elapsed > 3*time.Second {
		t.Errorf("放弃得太晚：%v（ReadHeaderTimeout 设的是 300ms）", elapsed)
	}
}

// TestDefaultHeaderLimit_AcceptsBrowserCookieLoad 锁定默认上限**装得下真实浏览器
// 发过来的 cookie**，而不是装得下我们自己以为的那点 cookie。
//
// 这条是 2026-09-09 那次 431 的回归测试。默认值曾是 8KiB，注释写的是「足以容纳常规
// cookie + UA + 自定义头」—— 那句话把 cookie 的量当成本站决定的了。实际上浏览器
// 按**域**发 cookie，一个挂在共享父域下的主机名会连父域上攒的一起发过来，真机量到
// 12~16KB。于是标准库在**任何 handler 之前**回 431 并关连接：不进路由、不进中间件、
// 一行日志都没有，前端看到的只是"打不开 / 账号密码不对"。
//
// ★ 判据必须是真连接：上限是标准库读连接时施加的，httptest.ResponseRecorder 结构性
// 测不到它（recorder 根本不经过读请求头这一段）。
func TestDefaultHeaderLimit_AcceptsBrowserCookieLoad(t *testing.T) {
	// 16KiB 取自真机：那台浏览器上 .ti.com 的 cookie 是 12~16KB。低于这个数就等于
	// 把"共享父域下的部署"排除在支持范围外 —— 而本仓的 workbench 出口正是那种部署。
	const realWorldCookieBytes = 16 << 10

	if DefaultMaxHeaderBytes < realWorldCookieBytes+2048 {
		t.Fatalf("DefaultMaxHeaderBytes=%d 装不下真实浏览器的 cookie（实测 12~16KB）+ 其余请求头；"+
			"调小它之前先读 transport/config.go 里那段注释", DefaultMaxHeaderBytes)
	}

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	applyServerLimits(srv, newConfig()) // 纯默认，不加任何 option

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go srv.Serve(ln)
	defer srv.Close()

	get := func(cookieBytes int) (int, error) {
		req, err := http.NewRequest("GET", "http://"+ln.Addr().String()+"/", nil)
		if err != nil {
			return 0, err
		}
		req.Header.Set("Cookie", "bloat="+strings.Repeat("a", cookieBytes))
		rsp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			return 0, err
		}
		defer rsp.Body.Close()
		_, _ = io.Copy(io.Discard, rsp.Body)
		return rsp.StatusCode, nil
	}

	code, err := get(realWorldCookieBytes)
	if err != nil {
		t.Fatalf("发 %dB cookie 出错: %v", realWorldCookieBytes, err)
	}
	if code != http.StatusOK {
		t.Fatalf("默认配置下 %dB 的 cookie 拿到 %d（431 = Request Header Fields Too Large）—— "+
			"真实浏览器在共享父域下就是发这么多", realWorldCookieBytes, code)
	}

	// 反向的闸：默认值是"够用"，不是"不限"。1MiB 仍然必须被拒，否则这条测试就成了
	// 一路放宽的许可证。
	code, err = get(1 << 20)
	if err == nil && code == http.StatusOK {
		t.Fatalf("1MiB 的请求头也被放行了 —— 默认上限形同虚设")
	}
}
