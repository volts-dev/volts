package router

import (
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/volts-dev/volts/transport"
)

/*
末段参数吞 `/` 不许抢在更具体的**兄弟**路由前面（见 matchNode 的 StaticNode 分支）。

同一位置上两个**名字不同**的参数是两个兄弟节点：

	/shop/brand/{slug}          没有子节点 → 余段带 `/` 时会整个吞下
	/shop/brand/{id}/logo

按注册顺序先试到 `{slug}`，它把 `1/logo` 吞掉，`{id}/logo` 永远打不到 —— 真栈上品牌 logo
端点从来没被调用过，每一张品牌 logo 都回了一整张网店页面，而 P0 基线把它记成了
"品牌不存在时回整页"（2026-09-29 站点应用挂载 P6 验收量到）。

同名参数（`category/{cat_id}` + `category/{cat_id}/image`）共用一个节点，一直是好的 ——
所以症状只在"恰好起了不同名字"时出现，看代码看不出来。

★ 只在兄弟之间比，不是"全树先试不吞的"：那一版让根级通配 `/*` 抢在所有吞 `/` 的路由
前面，静态文件 `/static/{:filepath}` 收 `css/site.css` 靠的正是吞 —— websvc（根级通配归
website）上整站样式脚本会落进 CMS 通配。TestSwallowStillBeatsCatchallElsewhere 钉着它。
*/

func siblingParamTree(t *testing.T, slugFirst bool) *TRouter {
	t.Helper()
	r := New()
	t.Cleanup(r.Close)
	g := NewGroup()
	mk := func(name string) func(*THttpContext) {
		return func(ctx *THttpContext) { ctx.Respond([]byte(name)) }
	}
	slug := func() { g.Url([]string{"GET"}, "/shop/brand/{slug}", mk("brand")) }
	logo := func() { g.Url([]string{"GET"}, "/shop/brand/{id}/logo", mk("logo")) }
	if slugFirst {
		slug()
		logo()
	} else {
		logo()
		slug()
	}
	g.Url([]string{"GET"}, "/shop/page/{page}", mk("page"))
	r.RegisterGroup(g)
	return r
}

func TestTrailingParamDoesNotShadowMoreSpecificSibling(t *testing.T) {
	for _, slugFirst := range []bool{true, false} {
		r := siblingParamTree(t, slugFirst)
		cases := []struct {
			path      string
			wantRoute string
			wantParam [2]string // 名字, 值
		}{
			// 更具体的兄弟赢，参数名是它自己的。
			{"/shop/brand/1/logo", "/shop/brand/{id}/logo", [2]string{"id", "1"}},
			// 只有一段时照旧落到 {slug}。
			{"/shop/brand/acme", "/shop/brand/{slug}", [2]string{"slug", "acme"}},
			// 没有更具体的兄弟时，末段参数照旧吞 `/`（树一直以来的行为，别处的用例钉着它；
			// 两趟只改"谁先"，不改"吞不吞"）。
			{"/shop/brand/1/other", "/shop/brand/{slug}", [2]string{"slug", "1/other"}},
			{"/shop/page/2/3", "/shop/page/{page}", [2]string{"page", "2/3"}},
		}
		for _, c := range cases {
			route, params := r.tree.Match("GET", c.path)
			if route == nil {
				t.Errorf("slugFirst=%v %s: 没匹配到", slugFirst, c.path)
				continue
			}
			if route.Path() != c.wantRoute {
				t.Errorf("slugFirst=%v %s → %s，期望 %s（参数 %v）",
					slugFirst, c.path, route.Path(), c.wantRoute, params)
				continue
			}
			got := ""
			for _, p := range params {
				if p.Name == c.wantParam[0] {
					got = p.Value
				}
			}
			if got != c.wantParam[1] {
				t.Errorf("slugFirst=%v %s: 参数 %s = %q，期望 %q（全部 %v）",
					slugFirst, c.path, c.wantParam[0], got, c.wantParam[1], params)
			}
		}
	}
}

// 候补（吞下来的那个）被兄弟比下去时，它压进去的参数不许串进兄弟的结果；候补最终胜出时，
// 它的参数要原样还回来。
func TestSwallowFallbackParamsAreClean(t *testing.T) {
	r := siblingParamTree(t, true)
	if _, params := r.tree.Match("GET", "/shop/brand/1/logo"); len(params) != 1 || params[0].Name != "id" {
		t.Fatalf("兄弟胜出，参数 %v —— 候补的 slug 串进来了", params)
	}
	if _, params := r.tree.Match("GET", "/shop/brand/1/other"); len(params) != 1 || params[0].Value != "1/other" {
		t.Fatalf("候补胜出，参数 %v", params)
	}
}

// 吞 `/` 只输给兄弟，不输给别处的通配：静态文件、`/shop/page/2/3` 这类仍归原来那条路由，
// 与改之前逐条相同（websvc 上根级通配 `/*` 与这些路由在同一棵树里）。
func TestSwallowStillBeatsCatchallElsewhere(t *testing.T) {
	fsys := fstest.MapFS{"static/css/site.css": &fstest.MapFile{Data: []byte("body{color:red}")}}
	r := New()
	t.Cleanup(r.Close)
	g := NewGroup()
	g.config.EmbedFS = fsys
	g.SetStatic("/static", t.TempDir())
	mk := func(name string) func(*THttpContext) {
		return func(ctx *THttpContext) { ctx.Respond([]byte(name)) }
	}
	g.Url([]string{"GET"}, "/shop/page/{page}", mk("page"))
	g.Url([]string{"GET"}, "/shop/{slug}", mk("product"))
	g.Url([]string{"GET"}, "/shop/brand/{slug}", mk("brand"))
	g.Url([]string{"GET"}, "/shop/brand/{id}/logo", mk("logo"))
	g.Url([]string{"GET", "POST"}, "/*", mk("catchall"))
	r.RegisterGroup(g)

	cases := []struct{ path, want string }{
		{"/static/css/site.css", "body{color:red}"}, // 吞 `/` 的静态路由，不能被 /* 抢走
		{"/shop/page/2/3", "page"},
		{"/shop/x/y", "product"},
		{"/shop/brand/a/b", "brand"},
		{"/shop/brand/1/logo", "logo"}, // 唯一变了的一条：兄弟赢
		{"/about", "catchall"},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, transport.NewHttpRequest(httptest.NewRequest("GET", c.path, nil)))
		if w.Body.String() != c.want {
			t.Errorf("%s → %d %q，期望 %q", c.path, w.Code, w.Body.String(), c.want)
		}
	}
}
