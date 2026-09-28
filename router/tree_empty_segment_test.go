package router

import (
	"net/http"
	"testing"
)

/*
参数段前面的路径正好用尽时，树不许 panic。

# 这是什么坑（2026-09-29 做站点应用挂载的 P1 时撞上）

`GET /shop/page/` —— `/shop/page/{page}` 注册着，余段是空串，matchNode 的 VariantNode
一支上来就取 `path[0]`：

	panic: runtime error: index out of range [0] with length 0

panic 发生在 `TRouter.ServeHTTP` 里、`Recover` 中间件**之前**（中间件在 handler 链上，
而这里还没匹配到 handler），所以它一路逃到 net/http 的 per-conn recover：客户端看到
连接被断（经网关是 502），进程日志里只有一条栈。**任何爬虫、任何人在地址栏随手补一个
斜杠**都能打到，而且每条带参数段的路由都有这个问题。

# 修法与期望行为

匹配不到就是匹配不到（返回 nil），后面由 TRouter 的尾斜杠归一接手 —— 它的判据是
"原路径匹配不到、而另一种写法匹配得到才跳"（trailing_slash_test.go）：

  - `/shop/page/`  → 去掉斜杠后是 `/shop/page`，那条没注册 → 404

# 顺带量到的一件事（**不在这一轮改**）

`/shop/page/2/` 不会 301，它直接 **200**，而且 `{page}` 的值是 `"2/"` —— 末尾的参数段
没有子节点时，树会把余下的字符连 `/` 一起吞掉。于是同一份内容有两个可达地址（SEO 侧是
重复内容），而 `strconv.Atoi("2/")` 失败通常被 handler 兜成第一页。

改它要动 matchNode 对所有带参数路由的既有行为，不属于挂载这一轮（P0–P5 不改变任何对外
行为）。挂载派发侧的对策写在 pattern.go 文件头第 2 条：匹配器一个参数只吃一段，派发处
自己按"原路径匹配不到、另一种写法匹配得到"去 301。这里把现状测着，它哪天变了会红。
*/

func newEmptySegRouter(t *testing.T) *TRouter {
	t.Helper()
	r := New()
	t.Cleanup(r.Close)
	grp := NewGroup()
	grp.Url([]string{"GET"}, "/shop/page/{page}", func(ctx *THttpContext) {
		ctx.Respond([]byte("page:" + ctx.PathParams().FieldByName("page").AsString()))
	})
	r.RegisterGroup(grp)
	return r
}

func TestTreeMatch_EmptySegmentBeforeVariantNode(t *testing.T) {
	tree := newEmptySegRouter(t).tree
	for _, path := range []string{"/shop/page/", "/shop/page//", "/shop/"} {
		rt, _ := tree.Match("GET", path) // 少了那道 path == "" 的闸，这里就是 panic
		if rt != nil {
			t.Errorf("%q 居然匹配到了路由 —— 空参数段不该命中", path)
		}
	}
}

func TestServeHTTP_TrailingSlashOnVariantRouteDoesNotPanic(t *testing.T) {
	r := newEmptySegRouter(t)

	// 参数段整个缺失：/shop/page 没注册，所以 404（而不是断连接）
	if w := do(t, r, "GET", "/shop/page/"); w.Code != http.StatusNotFound {
		t.Errorf("GET /shop/page/ 得到 %d（body=%q），期望 404", w.Code, w.Body.String())
	}

	// 参数在、只是多了个尾斜杠：树把斜杠吞进参数里（见文件头"顺带量到的一件事"）。
	// 这里钉的是**现状**，不是理想行为。
	w := do(t, r, "GET", "/shop/page/2/")
	if w.Code != http.StatusOK || w.Body.String() != "page:2/" {
		t.Errorf("GET /shop/page/2/ 得到 %d body=%q —— 树对末段参数的吞 `/` 行为变了，"+
			"去核对 pattern.go 文件头第 2 条与挂载派发侧的 301", w.Code, w.Body.String())
	}

	// 正常路径一个字不变
	if w := do(t, r, "GET", "/shop/page/2"); w.Code != http.StatusOK || w.Body.String() != "page:2" {
		t.Errorf("GET /shop/page/2 得到 %d body=%q", w.Code, w.Body.String())
	}
}
