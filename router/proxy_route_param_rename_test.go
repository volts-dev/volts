package router

import (
	"testing"

	"github.com/volts-dev/volts/registry"
)

// TestReclaim_ParamRenamedSamePosition：服务升级后同一位置的参数段改了名（`/courses/{id}` → `/courses/{course}`），
// 路径形状不变。上一轮的代理路由要被回收，这一轮的要还在、并且匹配得到 handler。
//
// 2026-10-15 真栈（vectors SEO S6）：websvc 把 `/courses/{id}`、`/forum/{forum_id}` 改名成 `{course}`、`{forum}`
// 之后，经 kylin 网关打 `/courses/<任何东西>` 一律回 **200 空正文**，直连 websvc 是对的——网关上那个位置
// 留着一条剥空了 handler 的路由，匹配走到它而不是新路由。
func TestReclaim_ParamRenamedSamePosition(t *testing.T) {
	r := newReclaimRouter(t)

	r.store([]*registry.Service{remoteService("websvc", "/courses/{id}", "/courses/{id}/slides/{slide_id}")})
	if !hasRoute(r, "/courses/x") {
		t.Fatal("测试前提不成立")
	}

	r.store([]*registry.Service{remoteService("websvc", "/courses/{course}", "/courses/{course}/{slide}")})

	for _, p := range []string{"/courses/x", "/courses/x/y"} {
		route, _ := r.tree.Match("GET", p)
		if route == nil {
			t.Fatalf("%s：改名之后匹配不到路由", p)
		}
		if len(route.Handlers()) == 0 {
			t.Fatalf("%s：匹配到的是一条剥空了 handler 的路由（真栈上就是 200 空正文）", p)
		}
	}
	// 旧形状按路由模式查（按请求路径查会被新的 {course}/{slide} 末段吞 `/` 匹配上，那是路由器的既定语义）。
	for _, old := range []string{"/courses/{id}", "/courses/{id}/slides/{slide_id}"} {
		if r.tree.RouteOf("GET", old) != nil {
			t.Errorf("旧形状 %s 已不再广告，代理路由仍在", old)
		}
	}

}
