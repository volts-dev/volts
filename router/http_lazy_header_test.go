package router

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/volts-dev/volts/transport"
)

func serveOnce(t *testing.T, method, path string, hd func(*THttpContext)) *httptest.ResponseRecorder {
	t.Helper()
	r := New()
	defer close(r.exit)
	grp := NewGroup()
	grp.Url([]string{method}, path, hd)
	r.RegisterGroup(grp)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, transport.NewHttpRequest(httptest.NewRequest(method, path, nil)))
	return rec
}

// TestApply_RespondByJsonAfterWriteHeaderKeepsBody 锁定这一条：
//
//	ctx.WriteHeader(422)
//	ctx.RespondByJson(x)
//
// 客户端必须拿到 422 **和** JSON 正文。旧实现里 WriteHeader 立刻发出状态行、Apply 再
// 看到 Written() 就把 Result 丢掉，客户端拿到的是 body 为空的 422 —— 编译通过、单测
// 照过，只有真栈 curl 看得出来（vectors 两个模块 2026-08-13 同一天踩到）。
func TestApply_RespondByJsonAfterWriteHeaderKeepsBody(t *testing.T) {
	rec := serveOnce(t, "POST", "/lazy/json", func(ctx *THttpContext) {
		ctx.WriteHeader(http.StatusUnprocessableEntity)
		ctx.RespondByJson(map[string]string{"error": "bad"})
	})
	if rec.Code != 422 {
		t.Fatalf("status=%d, want 422", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != `{"error":"bad"}` {
		t.Fatalf("body=%q, want the JSON payload", body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type=%q", ct)
	}
}

// TestApply_HeaderOnlyResponsesStillGoOut 锁定：只记了状态码没写正文的几种响应，
// 延迟提交之后仍然原样发出。
func TestApply_HeaderOnlyResponsesStillGoOut(t *testing.T) {
	cases := []struct {
		name string
		hd   func(*THttpContext)
		code int
		body string
		hdr  [2]string
	}{
		{"bare WriteHeader 404", func(ctx *THttpContext) { ctx.WriteHeader(404) }, 404, "", [2]string{}},
		{"NotModified", func(ctx *THttpContext) { ctx.NotModified() }, 304, "", [2]string{}},
		{"Redirect", func(ctx *THttpContext) { ctx.Redirect("/elsewhere") }, 302, "", [2]string{"Location", "/elsewhere"}},
		{"Abort with body", func(ctx *THttpContext) { ctx.Abort("nope", 401) }, 401, "nope", [2]string{}},
		{"NotFound with message", func(ctx *THttpContext) { ctx.NotFound("gone") }, 404, "gone", [2]string{}},
		{"RespondError", func(ctx *THttpContext) { ctx.RespondError("boom") }, 500, "boom\n", [2]string{}},
		{"plain Respond", func(ctx *THttpContext) { ctx.Respond([]byte("hi")) }, 200, "hi", [2]string{}},
		{"nothing at all", func(ctx *THttpContext) {}, 200, "", [2]string{}},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := serveOnce(t, "GET", "/lazy/case"+string(rune('a'+i)), c.hd)
			if rec.Code != c.code {
				t.Fatalf("status=%d, want %d", rec.Code, c.code)
			}
			if rec.Body.String() != c.body {
				t.Fatalf("body=%q, want %q", rec.Body.String(), c.body)
			}
			if c.hdr[0] != "" && rec.Header().Get(c.hdr[0]) != c.hdr[1] {
				t.Fatalf("%s=%q, want %q", c.hdr[0], rec.Header().Get(c.hdr[0]), c.hdr[1])
			}
		})
	}
}

// TestServeHTTP_UnmatchedRouteStill404 锁定：路由树没命中时的 404 走的是 router 自己的
// 早退路径（不经 Apply），延迟提交后那里必须显式 Commit。
func TestServeHTTP_UnmatchedRouteStill404(t *testing.T) {
	r := New()
	defer close(r.exit)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, transport.NewHttpRequest(httptest.NewRequest("GET", "/definitely/not/registered", nil)))
	if rec.Code != 404 {
		t.Fatalf("status=%d, want 404", rec.Code)
	}
}

// TestDeleteCookie_CarriesPath 锁定：删除指令必须带 Path（缺省 /），否则浏览器按当前
// 请求目录定域，与写入时 Path=/ 的那条不是同一个 Cookie，删除静默落空。
func TestDeleteCookie_CarriesPath(t *testing.T) {
	rec := serveOnce(t, "POST", "/shop/cart/update", func(ctx *THttpContext) {
		ctx.DeleteCookie("ws_promo", "")
		ctx.DeleteCookie("scoped", "/shop", "example.com")
	})
	cookies := rec.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("expected 2 Set-Cookie, got %d: %v", len(cookies), rec.Header().Values("Set-Cookie"))
	}
	if c := cookies[0]; c.Name != "ws_promo" || c.Path != "/" || c.MaxAge != -1 || c.Value != "" {
		t.Fatalf("default delete cookie wrong: %+v", c)
	}
	if c := cookies[1]; c.Path != "/shop" || c.Domain != "example.com" || c.MaxAge != -1 {
		t.Fatalf("scoped delete cookie wrong: %+v", c)
	}
}

// serveLive 把 router 挂在**真的** net/http 服务器后面。
//
// 必须用真服务器：httptest.ResponseRecorder 只是把 Header() 那个 map 记下来，
// 状态行发出之后写进去的头它照样看得见 —— 于是"头被冻住了"这件事在 recorder 上
// 完全测不出来。实测过：把 WriteHeader 改回立即透传，只用 recorder 的断言仍然全绿。
func serveLive(t *testing.T, method, path string, hd func(*THttpContext)) *http.Response {
	t.Helper()
	r := New()
	t.Cleanup(func() { close(r.exit) })
	grp := NewGroup()
	grp.Url([]string{method}, path, hd)
	r.RegisterGroup(grp)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.ServeHTTP(w, transport.NewHttpRequest(req))
	}))
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(method, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	rsp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rsp.Body.Close() })
	return rsp
}

// TestLive_HeadersSetAfterWriteHeaderStillGoOut 是上一条的真栈版本，钉的是**头没被
// 冻住**：WriteHeader 之后设的 Content-Type 与自定义头必须真的出现在连接上。
//
// 这正是延迟提交要解决的那一半 —— 状态行一发出，net/http 就冻结 Header()，此后所有
// Set 全部无声无息地丢掉。RespondByJson 恰好是在 WriteHeader 之后才设 Content-Type 的。
func TestLive_HeadersSetAfterWriteHeaderStillGoOut(t *testing.T) {
	rsp := serveLive(t, "POST", "/live/json", func(ctx *THttpContext) {
		ctx.WriteHeader(http.StatusUnprocessableEntity)
		ctx.Header().Set("X-Volts-Late", "1")
		ctx.RespondByJson(map[string]string{"error": "bad"})
	})

	if rsp.StatusCode != 422 {
		t.Fatalf("status=%d, want 422", rsp.StatusCode)
	}
	body, err := io.ReadAll(rsp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(body)); got != `{"error":"bad"}` {
		t.Fatalf("body=%q — 正文被丢了（旧行为：Apply 见 Written() 即跳过）", got)
	}
	if ct := rsp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type=%q — WriteHeader 之后设的头没出去，说明状态行提前发出冻结了头", ct)
	}
	if rsp.Header.Get("X-Volts-Late") != "1" {
		t.Fatalf("X-Volts-Late 丢失：%v", rsp.Header)
	}
}

// TestLive_HeaderOnly404GoesOut 钉真栈上的"只有状态码"响应：延迟提交后仍须发出 404，
// 而不是被 net/http 兜成隐式 200。
func TestLive_HeaderOnly404GoesOut(t *testing.T) {
	rsp := serveLive(t, "GET", "/live/404", func(ctx *THttpContext) {
		ctx.WriteHeader(http.StatusNotFound)
	})
	if rsp.StatusCode != 404 {
		t.Fatalf("status=%d, want 404", rsp.StatusCode)
	}
}

// TestApply_NoBodyOnStatusThatForbidsOne 锁定延迟提交带来的一个回归口子：
// 204 / 304 之后再 Respond，正文绝不能落笔。
//
// 延迟提交之前，`ctx.NotModified()` 立刻发出状态行，Apply 见 `Written()` 为真就跳过，
// 顺带把这条挡住了。改成延迟提交之后 Apply 的判据变成"写没写过正文"，于是 Result 会
// 一路走到 Write —— 实测这让 vectors 那边 connect_components 的
// `TestPassRevalidation_Answers304WithoutBody` 变红：**304 带了 18 字节正文出去**。
//
// ★ 这一条必须用 `httptest.ResponseRecorder`，不能用真服务器 —— 与本文件里
// TestLive_* 那几条的结论正好相反。真栈上 net/http 自己会把 304 的正文丢掉（实测
// 客户端收到 0 字节），所以真服务器测不出来；recorder 则照单全收。两种 writer 的判据
// 相反，而漏的正是**换一个 writer 之后**真的会漏出去的那一天（压缩包装、Hijack 之后
// 自己接管的连接）。
func TestApply_NoBodyOnStatusThatForbidsOne(t *testing.T) {
	cases := []struct {
		name string
		hd   func(*THttpContext)
		code int
	}{
		{"304 之后 Respond", func(ctx *THttpContext) {
			ctx.NotModified()
			ctx.Respond([]byte("这段不该出现"))
		}, http.StatusNotModified},
		{"204 之后 Respond", func(ctx *THttpContext) {
			ctx.WriteHeader(http.StatusNoContent)
			ctx.Respond([]byte("这段也不该出现"))
		}, http.StatusNoContent},
		{"304 之后 RespondByJson", func(ctx *THttpContext) {
			ctx.NotModified()
			ctx.RespondByJson(map[string]string{"a": "b"})
		}, http.StatusNotModified},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := serveOnce(t, "GET", "/nobody/case"+string(rune('a'+i)), c.hd)
			if rec.Code != c.code {
				t.Fatalf("status=%d, want %d", rec.Code, c.code)
			}
			if got := rec.Body.String(); got != "" {
				t.Fatalf("%d 带了 %d 字节正文：%q —— 浏览器对此各家表现不一", c.code, len(got), got)
			}
		})
	}
}

// TestApply_OrdinaryStatusStillCarriesBody 是上一条的反向断言：那道闸不许连累别人。
//
// 单独写出来是因为最容易写错的正是这个组合 —— 先 WriteHeader 一个非 200 的成功码、
// 再 Respond。判据放宽成"只要显式写过状态码就不发正文"的话，上一条照样绿，而这一条
// 立刻红。
func TestApply_OrdinaryStatusStillCarriesBody(t *testing.T) {
	rec := serveOnce(t, "GET", "/nobody/ordinary", func(ctx *THttpContext) {
		ctx.WriteHeader(http.StatusAccepted)
		ctx.Respond([]byte("hi"))
	})
	if rec.Code != http.StatusAccepted || rec.Body.String() != "hi" {
		t.Fatalf("status=%d body=%q，期望 202 与 \"hi\"", rec.Code, rec.Body.String())
	}
}
