package router

import (
	"bytes"
	"context"
	"io"
	stdlog "log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/volts-dev/volts/transport"
)

/*
同进程改派的契约（见 forward.go 文件头）。

判据分两层，缺一不可：

  - 参数/中间件/返回值这些**看得见的行为**，recorder 测得出；
  - "响应只被写了一次"**只有真服务器测得出** —— recorder 对重复 WriteHeader
    毫无反应，而真栈上那是 net/http 往 ErrorLog 里记的一条
    `superfluous response.WriteHeader call`。这条判据正是"改派要复用同一个
    context"的理由，所以它必须被钉住。
*/

// forwardRouter 造一棵有"通配 + 真路由"两层的树，通配那条按请求头决定改派到哪。
func forwardRouter(t *testing.T) *TRouter {
	t.Helper()
	r := New()
	t.Cleanup(r.Close)

	grp := NewGroup()
	// 真路由：一条静态、一条带参数、一条只收 POST。
	grp.Url([]string{"GET"}, "/shop/cart", func(ctx *THttpContext) {
		ctx.Respond([]byte("cart@" + ctx.Request().URL.Path))
	})
	grp.Url([]string{"GET"}, "/shop/page/{page}", func(ctx *THttpContext) {
		ctx.Respond([]byte("page:" + ctx.PathParams().FieldByName("page").AsString()))
	})
	grp.Url([]string{"POST"}, "/shop/cart/update", func(ctx *THttpContext) {
		ctx.Respond([]byte("updated"))
	})
	grp.Url([]string{"GET"}, "/shop/gone", func(ctx *THttpContext) {
		ctx.Abort("no such product", 404)
	})
	// 通配：改派目标由 X-Target 头给（模拟 vectors 那边"查挂载表"的那一步）。
	grp.Url([]string{"GET", "POST"}, "/*", func(ctx *THttpContext) {
		target := ctx.Request().Header().Get("X-Target")
		if target != "" && ctx.Forward(target) {
			return
		}
		ctx.Abort("catchall:"+ctx.Request().URL.Path, 404)
	})
	r.RegisterGroup(grp)
	return r
}

func forward(t *testing.T, r *TRouter, method, path, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if target != "" {
		req.Header.Set("X-Target", target)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, transport.NewHttpRequest(req))
	return w
}

func TestForwardRunsTargetRoute(t *testing.T) {
	r := forwardRouter(t)

	// 静态目标：handler 看到的是**目标路径**（它自己拼链接、解析路径都靠这个）。
	w := forward(t, r, "GET", "/store/cart", "/shop/cart")
	if w.Code != http.StatusOK || w.Body.String() != "cart@/shop/cart" {
		t.Errorf("改派到 /shop/cart 得到 %d body=%q", w.Code, w.Body.String())
	}

	// 带参数的目标：参数取自树的匹配结果，与直接请求 /shop/page/7 一模一样。
	w = forward(t, r, "GET", "/store/page/7", "/shop/page/7")
	if w.Code != http.StatusOK || w.Body.String() != "page:7" {
		t.Errorf("改派到 /shop/page/7 得到 %d body=%q —— 参数没按目标路由填", w.Code, w.Body.String())
	}
	if direct := forward(t, r, "GET", "/shop/page/7", ""); direct.Body.String() != w.Body.String() {
		t.Errorf("改派 %q 与直接请求 %q 结果不同", w.Body.String(), direct.Body.String())
	}
}

// 通配路由 `/*` 会往参数集里塞"余下的整段路径"。不整套换掉的话它就串进目标 handler，
// 而目标 handler 读自己的参数名读到的是上一条路由留下的值。
func TestForwardReplacesPathParamsWholesale(t *testing.T) {
	r := New()
	t.Cleanup(r.Close)
	grp := NewGroup()

	var seen []string
	grp.Url([]string{"GET"}, "/shop/cart", func(ctx *THttpContext) {
		seen = ctx.PathParams().Fields()
		ctx.Respond([]byte("ok"))
	})
	grp.Url([]string{"GET"}, "/*", func(ctx *THttpContext) {
		if !ctx.Forward("/shop/cart") {
			ctx.Abort("no target", 404)
		}
	})
	r.RegisterGroup(grp)

	if w := forward(t, r, "GET", "/store/cart", ""); w.Code != http.StatusOK {
		t.Fatalf("得到 %d", w.Code)
	}
	if len(seen) != 0 {
		t.Errorf("目标 handler 看到了来路通配留下的参数 %v —— /shop/cart 一个参数都不该有", seen)
	}
}

func TestForwardReportsMiss(t *testing.T) {
	r := forwardRouter(t)

	// 树上没有这条路径：Forward 返回 false 且一个字节都没写，调用方自己兜底。
	w := forward(t, r, "GET", "/store/nonesuch", "/shop/nonesuch")
	if w.Code != http.StatusNotFound || w.Body.String() != "catchall:/store/nonesuch" {
		t.Errorf("改派落空后得到 %d body=%q —— 期望回到调用方的兜底，且 URL.Path 已还原",
			w.Code, w.Body.String())
	}

	// 路径在、方法不对：同样是 false（树按方法匹配）。
	w = forward(t, r, "GET", "/store/cart/update", "/shop/cart/update")
	if w.Code != http.StatusNotFound || !strings.HasPrefix(w.Body.String(), "catchall:") {
		t.Errorf("方法不对的改派得到 %d body=%q —— 期望落空", w.Code, w.Body.String())
	}

	// 方法对上就跑得通（根级通配收 POST 不需要改框架，这里一并钉住）。
	if w := forward(t, r, "POST", "/store/cart/update", "/shop/cart/update"); w.Code != http.StatusOK ||
		w.Body.String() != "updated" {
		t.Errorf("POST 改派得到 %d body=%q", w.Code, w.Body.String())
	}
}

// 目标 handler 自己 Abort 的状态码要原样出去 —— 改派不吞、不改写。
func TestForwardKeepsTargetStatus(t *testing.T) {
	r := forwardRouter(t)
	w := forward(t, r, "GET", "/store/gone", "/shop/gone")
	if w.Code != http.StatusNotFound || w.Body.String() != "no such product" {
		t.Errorf("得到 %d body=%q —— 期望目标那条 404 原样出去", w.Code, w.Body.String())
	}
}

// 改派回自己 = 环。必须当场拒，而不是等到栈溢出。
func TestForwardRefusesSelfAndCycles(t *testing.T) {
	r := New()
	t.Cleanup(r.Close)
	grp := NewGroup()
	calls := 0
	grp.Url([]string{"GET"}, "/*", func(ctx *THttpContext) {
		calls++
		if ctx.Forward("/anything") { // 还是落在 /* 上
			return
		}
		ctx.Abort("refused", 404)
	})
	r.RegisterGroup(grp)

	if w := forward(t, r, "GET", "/x", ""); w.Code != http.StatusNotFound || w.Body.String() != "refused" {
		t.Errorf("得到 %d body=%q", w.Code, w.Body.String())
	}
	if calls != 1 {
		t.Errorf("通配 handler 被调了 %d 次 —— 改派回同一条路由必须当场拒", calls)
	}
}

// 相对路径不是合法目标（改派是树上的绝对路径）。
func TestForwardRejectsRelativeTarget(t *testing.T) {
	r := forwardRouter(t)
	if w := forward(t, r, "GET", "/store/cart", "shop/cart"); w.Code != http.StatusNotFound {
		t.Errorf("相对路径目标得到 %d —— 期望落空", w.Code)
	}
}

// 会话/身份挂在 ctx.context 上（vectors 的 AuthSession 走这条）。改派复用同一个
// context，正是为了让外层中间件解好的身份能到目标 handler 手里。
func TestForwardCarriesContextValues(t *testing.T) {
	r := New()
	t.Cleanup(r.Close)
	grp := NewGroup()
	got := "<missing>"
	grp.Url([]string{"GET"}, "/shop/cart", func(ctx *THttpContext) {
		if v, ok := ctx.Context().Value(ctxKeyProbe{}).(string); ok {
			got = v
		}
		ctx.Respond([]byte("ok"))
	})
	grp.Url([]string{"GET"}, "/*", func(ctx *THttpContext) {
		ctx.Context(context.WithValue(ctx.Context(), ctxKeyProbe{}, "session-42"))
		if !ctx.Forward("/shop/cart") {
			ctx.Abort("miss", 404)
		}
	})
	r.RegisterGroup(grp)

	forward(t, r, "GET", "/store/cart", "")
	if got != "session-42" {
		t.Errorf("目标 handler 取到的上下文值是 %q —— 改派丢了会话", got)
	}
}

type ctxKeyProbe struct{}

// ★ 这条只有**真服务器**测得出：recorder 对重复 WriteHeader 毫无反应。
//
// 改派若是"再进一次 ServeHTTP"，外层那个 context 的 Apply 会补一次 WriteHeader(200)，
// net/http 就往 Server.ErrorLog 记一条 superfluous —— 响应看着是对的，日志被刷烂。
func TestForwardDoesNotLogSuperfluousWriteHeader(t *testing.T) {
	r := forwardRouter(t)

	var mu sync.Mutex
	var errlog bytes.Buffer
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.ServeHTTP(w, transport.NewHttpRequest(req))
	}))
	srv.Config.ErrorLog = stdlog.New(&lockedWriter{mu: &mu, w: &errlog}, "", 0)
	srv.Start()
	t.Cleanup(srv.Close)

	req, err := http.NewRequest("GET", srv.URL+"/store/cart", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Target", "/shop/cart")
	rsp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer rsp.Body.Close()
	body, _ := io.ReadAll(rsp.Body)

	if rsp.StatusCode != http.StatusOK || string(body) != "cart@/shop/cart" {
		t.Errorf("真栈上得到 %d body=%q", rsp.StatusCode, string(body))
	}
	mu.Lock()
	logged := errlog.String()
	mu.Unlock()
	if strings.Contains(logged, "superfluous") {
		t.Errorf("net/http 记了重复 WriteHeader：%s\n"+
			"—— 一次请求写了两次响应，说明改派没有复用同一个 context", logged)
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (self *lockedWriter) Write(p []byte) (int, error) {
	self.mu.Lock()
	defer self.mu.Unlock()
	return self.w.Write(p)
}

// 守卫的自测：上面那条断言只有在"net/http 真的会把 superfluous 记进 Server.ErrorLog"
// 时才有意义。这里手工写两次响应头，证明这条通道是通的 —— 否则上面那条会永远绿。
func TestSuperfluousDetectionItself(t *testing.T) {
	var mu sync.Mutex
	var errlog bytes.Buffer
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(200)
		w.WriteHeader(200) // 第二次：net/http 记 superfluous
	}))
	srv.Config.ErrorLog = stdlog.New(&lockedWriter{mu: &mu, w: &errlog}, "", 0)
	srv.Start()
	t.Cleanup(srv.Close)

	rsp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	rsp.Body.Close()

	mu.Lock()
	logged := errlog.String()
	mu.Unlock()
	if !strings.Contains(logged, "superfluous") {
		t.Skipf("这个 Go 版本没把重复 WriteHeader 记进 Server.ErrorLog（记到了 %q）—— "+
			"TestForwardDoesNotLogSuperfluousWriteHeader 这条判据在此失效，换判据再钉", logged)
	}
}
