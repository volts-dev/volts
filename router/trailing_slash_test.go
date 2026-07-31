package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/volts-dev/volts/transport"
)

// 尾斜杠规范化。
//
// 起因是真栈上一次报障："/shop/checkout 打得开，/shop/checkout/ 404"。框架此前
// **从不处理尾斜杠**，也就是说每一条路由都有这个问题——而地址栏、书签、外链、爬虫
// 随手补一个斜杠是常态，表现又恰好是"这个页面不存在"，很难往路由上想。
//
// 规则只有一条：**原路径匹配不到、而另一种写法匹配得到时**才跳。今天能用的 URL
// 行为一概不变。
func newSlashRouter(t *testing.T) *TRouter {
	t.Helper()
	r := New()
	t.Cleanup(func() { close(r.exit) })

	grp := NewGroup()
	grp.Url([]string{"GET"}, "/shop/checkout", func(ctx *THttpContext) {
		ctx.Respond([]byte("checkout"))
	})
	grp.Url([]string{"POST"}, "/shop/checkout", func(ctx *THttpContext) {
		ctx.Respond([]byte("posted"))
	})
	// 注册时**自带**尾斜杠：树在注册那一刻就规范成 `/docs`。
	grp.Url([]string{"GET"}, "/docs/", func(ctx *THttpContext) {
		ctx.Respond([]byte("docs"))
	})
	// 同层通配路由——真栈上正是它把 `/shop/checkout/` 接走，回了 "product not found"。
	grp.Url([]string{"GET"}, "/shop/{slug}", func(ctx *THttpContext) {
		ctx.Respond([]byte("slug:" + ctx.PathParams().FieldByName("slug").AsString()))
	})
	r.RegisterGroup(grp)
	return r
}

func do(t *testing.T, r *TRouter, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, transport.NewHttpRequest(req))
	return w
}

func TestTrailingSlashRedirectsToRegisteredPath(t *testing.T) {
	r := newSlashRouter(t)

	w := do(t, r, "GET", "/shop/checkout/")
	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("GET 带尾斜杠应 301，得到 %d", w.Code)
	}
	if got := w.Header().Get("Location"); got != "/shop/checkout" {
		t.Errorf("Location = %q，期望 /shop/checkout", got)
	}
}

// 查询串必须原样带过去，否则 /shop/page/2/?search=desk 这类链接跳完就丢了条件。
func TestTrailingSlashKeepsQuery(t *testing.T) {
	r := newSlashRouter(t)

	w := do(t, r, "GET", "/shop/checkout/?search=desk&order=name+asc")
	if got := w.Header().Get("Location"); got != "/shop/checkout?search=desk&order=name+asc" {
		t.Errorf("查询串丢了: %q", got)
	}
}

// 非 GET 必须用 308：301/302 允许客户端把方法改写成 GET，带表单的 POST 会**静默
// 丢掉 body**——用户看到的是"提交没反应"，比 404 更难查。
func TestTrailingSlashPostUses308(t *testing.T) {
	r := newSlashRouter(t)

	w := do(t, r, "POST", "/shop/checkout/")
	if w.Code != http.StatusPermanentRedirect {
		t.Fatalf("POST 带尾斜杠应 308（保方法保 body），得到 %d", w.Code)
	}
}

// 注册路径自带尾斜杠时（`/docs/`）：**树在注册那一刻就把它规范化成 `/docs`**，
// 所以 `/docs` 直接 200，而 `/docs/` 走本次新增的跳转回到 `/docs`。
//
// 也就是说框架其实只在**请求侧**缺这一段，注册侧一直是规范化的——这也解释了为什么
// 这个缺陷能潜伏这么久：写路由的人从没觉得两种写法有区别。
func TestRegisteredPathWithSlashIsNormalized(t *testing.T) {
	r := newSlashRouter(t)

	if w := do(t, r, "GET", "/docs"); w.Code != http.StatusOK {
		t.Fatalf("/docs 应直接 200（注册侧已规范化），得到 %d", w.Code)
	}
	w := do(t, r, "GET", "/docs/")
	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("/docs/ 应 301 回 /docs，得到 %d", w.Code)
	}
	if got := w.Header().Get("Location"); got != "/docs" {
		t.Errorf("Location = %q，期望 /docs", got)
	}
}

// 完全没注册的路径 → 老老实实 404。**绝不能无条件跳**，否则会在两种写法之间
// 来回弹，浏览器报重定向循环，比 404 难查得多。
func TestUnknownPathStill404(t *testing.T) {
	r := newSlashRouter(t)

	for _, p := range []string{"/nope", "/nope/"} {
		if w := do(t, r, "GET", p); w.Code != http.StatusNotFound {
			t.Errorf("%s 应 404，得到 %d（Location=%q）", p, w.Code, w.Header().Get("Location"))
		}
	}
}

// **本次的核心用例**：带尾斜杠时掉进同层通配路由。判据不是"匹配没匹配上"——它
// 匹配上了，只是匹配错了——而是"去掉尾斜杠后是不是落到另一条路由"。
func TestTrailingSlashSwallowedByWildcardIsRedirected(t *testing.T) {
	r := newSlashRouter(t)

	// 没有规范化时，这条会被 /shop/{slug} 接走（slug="checkout/"），
	// 真栈上的表现就是商品页回一句 "product not found"。
	w := do(t, r, "GET", "/shop/checkout/")
	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("应跳回静态路由，得到 %d body=%q", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Location"); got != "/shop/checkout" {
		t.Errorf("Location = %q", got)
	}
}

// 反面：去掉尾斜杠后**还是同一条通配路由**时不许跳——否则每个以斜杠结尾的
// 通配路径都会被无谓地弹一次（静态资源目录首当其冲）。
func TestWildcardOnlyPathIsNotRedirected(t *testing.T) {
	r := newSlashRouter(t)

	w := do(t, r, "GET", "/shop/some-product/")
	if w.Code == http.StatusMovedPermanently {
		t.Fatalf("同属一条通配路由，不该跳转（Location=%q）", w.Header().Get("Location"))
	}
	if w.Code != http.StatusOK {
		t.Fatalf("应由通配路由正常处理，得到 %d", w.Code)
	}
}

// 能用的 URL 一律不受影响。
func TestExactPathUnaffected(t *testing.T) {
	r := newSlashRouter(t)

	w := do(t, r, "GET", "/shop/checkout")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "checkout") {
		t.Fatalf("原本能用的路径被改变了: %d %s", w.Code, w.Body.String())
	}
}

// 根路径 "/" 去掉尾斜杠就成了空路径，绝不能跳。
func TestRootPathNotRewritten(t *testing.T) {
	r := newSlashRouter(t)

	if w := do(t, r, "GET", "/"); w.Code == http.StatusMovedPermanently {
		t.Errorf("根路径不该被规范化（Location=%q）", w.Header().Get("Location"))
	}
}
