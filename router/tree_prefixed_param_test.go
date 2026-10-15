package router

import "testing"

// 一段里"字面前缀 + 参数"（/web/image/website.{name}）是静态节点打头，先于同位置的参数兄弟（/web/image/{id}）试，
// 与注册先后无关。网关靠它把 website 的公开示例图（/web/image/website.s_banner_default_image）转给 websvc，而不是落到
// 本进程要登录的 /web/image/{id} 上。
func TestPrefixedParamBeatsPlainParamSibling(t *testing.T) {
	for _, prefixedFirst := range []bool{true, false} {
		r := New()
		g := NewGroup()
		mk := func(name string) func(*THttpContext) { return func(ctx *THttpContext) { ctx.Respond([]byte(name)) } }
		pre := func() { g.Url([]string{"GET"}, "/web/image/website.{name}", mk("pre")) }
		plain := func() { g.Url([]string{"GET"}, "/web/image/{id}", mk("plain")) }
		if prefixedFirst {
			pre()
			plain()
		} else {
			plain()
			pre()
		}
		r.RegisterGroup(g)
		route, params := r.tree.Match("GET", "/web/image/website.s_banner_default_image")
		if route == nil || route.Path() != "/web/image/website.{name}" || params.Get("name") != "s_banner_default_image" {
			t.Errorf("prefixedFirst=%v：website.* 落到 %v（参数 %v）", prefixedFirst, route, params)
		}
		for _, p := range []string{"/web/image/2110306285061160960", "/web/image/2101461724375814144-616ce524", "/web/image/mass_mailing.x"} {
			if route, _ := r.tree.Match("GET", p); route == nil || route.Path() != "/web/image/{id}" {
				t.Errorf("prefixedFirst=%v：%s 要落到 /web/image/{id}，得 %v", prefixedFirst, p, route)
			}
		}
		r.Close()
	}
}
