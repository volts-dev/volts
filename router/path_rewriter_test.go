package router

import (
	"net/http"
	"strings"
	"testing"
)

// 路由匹配前的路径改写（PathRewriter），多语言 URL 前缀的落点。
//
// 关键在于**匹配之前**：middleware 是装配期挂到 handler 链上的，跑到它时路由已经
// 匹配完了。所以 /fr/shop 若不在这里摘掉前缀，要么匹配不上（404），要么各模块都得
// 把自己的前台路由再注册一份 /{lang}/...——路由表和注册到 registry 的端点表双倍
// 膨胀，且根部的单段通配会抢走所有二级路径（/company/team 被读成 lang=company）。

// langRewriter 只认已知的语言前缀，其余路径原样放过——未知前缀不能被当成语言，
// 否则 /company/team 这种二级路径会被拆成 lang=company + /team。
func langRewriter(codes ...string) PathRewriter {
	return func(path string) (string, string) {
		trimmed := strings.TrimPrefix(path, "/")
		seg := trimmed
		if i := strings.IndexByte(trimmed, '/'); i >= 0 {
			seg = trimmed[:i]
		}
		for _, c := range codes {
			if seg == c {
				after := strings.TrimPrefix(path, "/"+seg)
				if after == "" {
					after = "/"
				}
				return after, "/" + seg
			}
		}
		return path, ""
	}
}

type rewriteProbe struct {
	path   string
	prefix string
	hits   int
}

func newRewriteRouter(t *testing.T, rw PathRewriter, probe *rewriteProbe) *TRouter {
	t.Helper()
	r := New(WithPathRewriter(rw))
	t.Cleanup(func() { close(r.exit) })

	grp := NewGroup()
	record := func(ctx *THttpContext) {
		probe.hits++
		probe.path = ctx.Request().URL.Path
		probe.prefix = ctx.PathPrefix()
		ctx.Respond([]byte("ok"))
	}
	// 注意：**只注册一次**，没有 /{lang}/shop/checkout 这份。
	grp.Url([]string{"GET"}, "/shop/checkout", record)
	grp.Url([]string{"GET"}, "/shop/{slug}", record)
	r.RegisterGroup(grp)
	return r
}

// 模块的路由注册不必改动，带语言前缀的 URL 也能命中——这就是整个钩子的目的。
func TestPathRewriter_PrefixedPathMatchesUnchangedRoute(t *testing.T) {
	probe := &rewriteProbe{}
	r := newRewriteRouter(t, langRewriter("fr", "zh-cn"), probe)

	w := do(t, r, "GET", "/fr/shop/checkout")
	if w.Code != http.StatusOK {
		t.Fatalf("/fr/shop/checkout 应命中 /shop/checkout，得到 %d", w.Code)
	}
	// handler 看到的必须是去前缀的路径，否则所有既有 handler 的 path 判断都要改。
	if probe.path != "/shop/checkout" {
		t.Errorf("handler 看到的 path = %q，期望 /shop/checkout", probe.path)
	}
	if probe.prefix != "/fr" {
		t.Errorf("PathPrefix() = %q，期望 /fr", probe.prefix)
	}
}

// 只有语言前缀那一段被摘掉，路由参数照常解析。
func TestPathRewriter_PathParamsStillResolve(t *testing.T) {
	probe := &rewriteProbe{}
	r := New(WithPathRewriter(langRewriter("fr")))
	t.Cleanup(func() { close(r.exit) })

	var slug string
	grp := NewGroup()
	grp.Url([]string{"GET"}, "/shop/{slug}", func(ctx *THttpContext) {
		slug = ctx.PathParams().FieldByName("slug").AsString()
		ctx.Respond([]byte("ok"))
	})
	r.RegisterGroup(grp)

	if w := do(t, r, "GET", "/fr/shop/desk-42"); w.Code != http.StatusOK {
		t.Fatalf("状态 %d", w.Code)
	}
	if slug != "desk-42" {
		t.Errorf("slug = %q，期望 desk-42", slug)
	}
	_ = probe
}

// 未知的第一段不是语言，不能被摘掉——否则任意二级路径都会被拆成
// "前缀 + 剩下"，把 /company/team 读成 lang=company + /team，还给爬虫送出
// 无限份重复内容。
func TestPathRewriter_UnknownPrefixNotStripped(t *testing.T) {
	probe := &rewriteProbe{}
	r := newRewriteRouter(t, langRewriter("fr"), probe)

	w := do(t, r, "GET", "/company/team")
	if w.Code != http.StatusNotFound {
		t.Fatalf("未知前缀不该被摘掉，期望 404，得到 %d", w.Code)
	}
	if probe.hits != 0 {
		t.Errorf("handler 不该被调用，hits=%d", probe.hits)
	}
}

// 尾斜杠重定向必须把语言前缀拼回去。摘掉前缀后直接跳去 trimmed，
// `/fr/shop/checkout/` 会被 301 到 `/shop/checkout` —— 语言静默丢掉，
// 而且这条永久跳转会被浏览器和 CDN 缓存下来。
func TestPathRewriter_TrailingSlashRedirectKeepsPrefix(t *testing.T) {
	probe := &rewriteProbe{}
	r := New(WithPathRewriter(langRewriter("fr")))
	t.Cleanup(func() { close(r.exit) })

	grp := NewGroup()
	grp.Url([]string{"GET"}, "/shop/checkout", func(ctx *THttpContext) {
		probe.hits++
		ctx.Respond([]byte("ok"))
	})
	// 同层通配：没有它，尾斜杠路径不会"落到另一条路由"，也就不触发规范化跳转。
	grp.Url([]string{"GET"}, "/shop/{slug}", func(ctx *THttpContext) {
		ctx.Respond([]byte("slug"))
	})
	r.RegisterGroup(grp)

	w := do(t, r, "GET", "/fr/shop/checkout/")
	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("期望 301，得到 %d", w.Code)
	}
	if got := w.Header().Get("Location"); got != "/fr/shop/checkout" {
		t.Errorf("Location = %q，期望 /fr/shop/checkout（前缀丢了就掉语言）", got)
	}
}

// context 按路由池化复用，pathPrefix 必须在 reset 里清掉：上一轮带 /fr、这一轮
// 不带时，残留的前缀会让链接生成和重定向凭空多出一门语言。
func TestPathRewriter_PrefixNotLeakedAcrossPooledContexts(t *testing.T) {
	probe := &rewriteProbe{}
	r := newRewriteRouter(t, langRewriter("fr"), probe)

	if w := do(t, r, "GET", "/fr/shop/checkout"); w.Code != http.StatusOK {
		t.Fatalf("第一次请求状态 %d", w.Code)
	}
	if probe.prefix != "/fr" {
		t.Fatalf("第一次 PathPrefix() = %q，期望 /fr", probe.prefix)
	}

	// 同一条路由 → 同一个 context 池 → 取到的是上一轮那个对象。
	if w := do(t, r, "GET", "/shop/checkout"); w.Code != http.StatusOK {
		t.Fatalf("第二次请求状态 %d", w.Code)
	}
	if probe.prefix != "" {
		t.Errorf("复用的 context 残留了前缀 %q，reset 没清", probe.prefix)
	}
}

// 没装钩子时行为一字不变——它是可选能力，绝大多数 router 实例不会有 rewriter。
func TestPathRewriter_AbsentHookChangesNothing(t *testing.T) {
	probe := &rewriteProbe{}
	r := newRewriteRouter(t, nil, probe)

	if w := do(t, r, "GET", "/shop/checkout"); w.Code != http.StatusOK {
		t.Fatalf("状态 %d", w.Code)
	}
	if probe.path != "/shop/checkout" || probe.prefix != "" {
		t.Errorf("path=%q prefix=%q，期望 /shop/checkout 与空前缀", probe.path, probe.prefix)
	}
	// 带前缀的路径在没有 rewriter 时就是 404，这也是"不改框架就绕不过去"的证据。
	if w := do(t, r, "GET", "/fr/shop/checkout"); w.Code != http.StatusNotFound {
		t.Errorf("无 rewriter 时 /fr/... 应 404，得到 %d", w.Code)
	}
}

// 钩子返回空 stripped 表示"没改写"，此时即便 newPath 变了也不采纳——
// 让"不改写"只有一种表达，避免调用方两个返回值给得不一致时行为含糊。
func TestPathRewriter_EmptyStrippedMeansNoRewrite(t *testing.T) {
	probe := &rewriteProbe{}
	r := newRewriteRouter(t, func(path string) (string, string) {
		return "/shop/checkout", "" // newPath 给了，stripped 空
	}, probe)

	if w := do(t, r, "GET", "/fr/shop/checkout"); w.Code != http.StatusNotFound {
		t.Fatalf("stripped 为空应视为未改写，期望 404，得到 %d", w.Code)
	}
}
