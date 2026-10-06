package router

import "testing"

// RouteOf / DelRoute 对 CONNECT 路由必须与 AddRoute 用同一个分隔符。
//
// 此前两者给 CONNECT 写死了 '.'，而 AddRoute 按路由自带的 '/' 解析：`/sale.order.action_confirm`
// 在树里是一段，查找时被切成三段 —— RPC 路由一律「查不到、删不掉」，且不报错。
// vectors 的启动期守卫（贡献声明的端点有没有注册）靠 RouteOf 判断，查不到就会对每一条
// 正确的声明误报。
func TestRouteOf_FindsAndDeletesConnectRoutes(t *testing.T) {
	tree := NewRouteTree()
	r := newRoute(nil, []string{"CONNECT"}, &TUrl{Path: "/sale.order.action_confirm"}, "/sale.order.action_confirm", "", "", "")
	if err := tree.AddRoute(r); err != nil {
		t.Fatal(err)
	}
	if got := tree.RouteOf("CONNECT", "/sale.order.action_confirm"); got != r {
		t.Fatalf("RouteOf 找不到刚加的 CONNECT 路由（得到 %v）", got)
	}
	if tree.RouteOf("CONNECT", "/sale.order.action_cancel") != nil {
		t.Fatal("RouteOf 对没注册的路径不该命中")
	}
	if err := tree.DelRoute("/sale.order.action_confirm", r); err != nil {
		t.Fatal(err)
	}
	if tree.RouteOf("CONNECT", "/sale.order.action_confirm") != nil {
		t.Fatal("DelRoute 之后 CONNECT 路由还在")
	}
}
