package router

import "testing"

// 同一位置上**带类型**的参数（{id:int}）排在无类型参数（{xmlid}）前面试，与注册先后无关。
//
// 2026-10-15 真栈：web 模块的 /web/image/{id}（要登录）与 website 的 /web/image/{xmlid}（公开示例图）同形；
// 网关上前者是本地路由先挂上，经网关的 /web/image/website.s_banner_default_image 一律 401，CMS 页上的示例图对匿名
// 访客全是裂图。把前者写成 {id:int} 之后，数字 id 归它、xmlid 落到兄弟上——前提是兄弟之间按"更具体的先试"排，
// 而不是看谁先插入（sort.Sort 对同级节点不保证次序）。
func TestTypedParamTriedBeforeUntypedSibling(t *testing.T) {
	for _, typedFirst := range []bool{true, false} {
		r := New()
		g := NewGroup()
		mk := func(name string) func(*THttpContext) { return func(ctx *THttpContext) { ctx.Respond([]byte(name)) } }
		typed := func() { g.Url([]string{"GET"}, "/web/image/{id:int}", mk("id")) }
		plain := func() { g.Url([]string{"GET"}, "/web/image/{xmlid}", mk("xmlid")) }
		if typedFirst {
			typed()
			plain()
		} else {
			plain()
			typed()
		}
		r.RegisterGroup(g)
		for path, want := range map[string]string{
			"/web/image/2110306285061160960":           "/web/image/{id:int}",
			"/web/image/website.s_banner_default_image": "/web/image/{xmlid}",
		} {
			route, _ := r.tree.Match("GET", path)
			if route == nil || route.Path() != want {
				got := "<nil>"
				if route != nil {
					got = route.Path()
				}
				t.Errorf("typedFirst=%v %s → %s，期望 %s", typedFirst, path, got, want)
			}
		}
		r.Close()
	}
}
