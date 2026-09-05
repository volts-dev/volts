package router

import (
	"testing"

	"github.com/volts-dev/volts/registry"
)

// newReclaimRouter 建一个能认出自己的 router —— store() 认不出自己时会整轮跳过
// （`me == nil` 提前返回），什么都不会建，测试也就无从谈起。
func newReclaimRouter(t *testing.T) *TRouter {
	t.Helper()
	cacher := registry.NewCacher(&echoRegistry{})
	r := New(WithRegistry(cacher))
	t.Cleanup(r.Close)

	if err := cacher.Register(&registry.Service{
		Name:    "registry",
		Version: "latest",
		Nodes:   []*registry.Node{{Id: "gw-http", Address: "127.0.0.1:35999"}},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	return r
}

func remoteService(name string, paths ...string) *registry.Service {
	eps := make([]*registry.Endpoint, 0, len(paths))
	for _, p := range paths {
		eps = append(eps, &registry.Endpoint{Name: name + p, Method: []string{"GET"}, Path: p})
	}
	return &registry.Service{
		Name:      name,
		Version:   "latest",
		Nodes:     []*registry.Node{{Id: name + "-remote", Address: "192.0.2.10:36099"}},
		Endpoints: eps,
	}
}

func hasRoute(r *TRouter, path string) bool {
	route, _ := r.tree.Match("GET", path)
	return route != nil
}

// TestReclaim_ServiceGoneDropsItsRoutes 锁定主路径：服务从注册中心整个消失后，
// 它的代理路由要被收回来。
//
// 不收的后果不是"多一条没用的路由"：请求打过来选不到节点，答的是 **503**，而正确
// 答案是 404 —— 这条路径已经不存在了。503 的语义是"稍后重试"，客户端和网关都会照做，
// 于是一条永远不会回来的路径被反复重试。`store()` 此前只增不减，`TRouter.Deregister`
// 在整个仓里只有测试在调。
func TestReclaim_ServiceGoneDropsItsRoutes(t *testing.T) {
	r := newReclaimRouter(t)

	r.store([]*registry.Service{remoteService("sale", "/sale/a", "/sale/b")})
	if !hasRoute(r, "/sale/a") || !hasRoute(r, "/sale/b") {
		t.Fatal("代理路由没建起来，测试前提不成立")
	}

	r.dropProxyRoutes("sale")

	if hasRoute(r, "/sale/a") || hasRoute(r, "/sale/b") {
		t.Error("服务已消失，代理路由仍留在树里 —— 这些路径会一直答 503 而不是 404")
	}
}

// TestReclaim_EndpointRemovedInNextRound 锁定差量回收：服务还在，但升级后砍掉了一个
// 端点，那一条要收回，其余不动。
func TestReclaim_EndpointRemovedInNextRound(t *testing.T) {
	r := newReclaimRouter(t)

	r.store([]*registry.Service{remoteService("sale", "/sale/keep", "/sale/gone")})
	if !hasRoute(r, "/sale/keep") || !hasRoute(r, "/sale/gone") {
		t.Fatal("测试前提不成立")
	}

	// 新版本不再广告 /sale/gone
	r.store([]*registry.Service{remoteService("sale", "/sale/keep")})

	if hasRoute(r, "/sale/gone") {
		t.Error("下线的端点仍有代理路由")
	}
	if !hasRoute(r, "/sale/keep") {
		t.Error("还在广告的端点被误删了")
	}
}

// TestReclaim_NeverTouchesLocalRoute 锁定最重要的安全边界：回收绝不能碰本地路由。
//
// 树里同路径是后写覆盖，所以"先有代理路由、后被本地路由顶掉"是真实存在的顺序。此时
// 我们记的那个 handler id 早已不在树上，若照删不误，`DelRoute` 什么也剥不掉却会把
// `TTree.Count` 减一，而更糟的情形是把本地 handler 一起剥掉。
func TestReclaim_NeverTouchesLocalRoute(t *testing.T) {
	r := newReclaimRouter(t)

	const path = "/shared/thing"
	r.store([]*registry.Service{remoteService("sale", path)})
	if !hasRoute(r, path) {
		t.Fatal("测试前提不成立")
	}

	// 本地模块随后在同一路径挂上自己的 handler（后写覆盖）。
	grp := NewGroup()
	grp.Url([]string{"GET"}, path, func(ctx *THttpContext) {})
	r.RegisterGroup(grp)

	local, _ := r.tree.Match("GET", path)
	if local == nil {
		t.Fatal("本地路由没挂上，测试前提不成立")
	}
	localHandlers := append([]int(nil), local.Handlers()...)
	if len(localHandlers) == 0 {
		t.Fatal("本地路由没有 handler，测试前提不成立")
	}

	r.dropProxyRoutes("sale")

	after, _ := r.tree.Match("GET", path)
	if after == nil {
		t.Fatal("回收把本地路由一起删掉了")
	}
	if after.Id() != local.Id() {
		t.Errorf("本地路由被动过：id %v → %v", local.Id(), after.Id())
	}
	for _, want := range localHandlers {
		var found bool
		for _, got := range after.Handlers() {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("本地 handler %d 被剥掉了，剩下 %v", want, after.Handlers())
		}
	}
}

// TestReclaim_KeepsPathClaimedByAnotherService 锁定：两个服务广告同一条路径时，
// 其中一个消失不能把这条路径删掉 —— 树里同路径只有一条路由，删了会把还活着的那个
// 一起打掉，要等下一轮 refresh（最多 60s）才补回来。
func TestReclaim_KeepsPathClaimedByAnotherService(t *testing.T) {
	r := newReclaimRouter(t)

	const path = "/both/serve"
	r.store([]*registry.Service{remoteService("sale", path)})
	r.store([]*registry.Service{remoteService("purchase", path)})
	if !hasRoute(r, path) {
		t.Fatal("测试前提不成立")
	}

	// ★ 必须下线**当前挂在树上的那个**（后写覆盖，所以是 purchase）。
	// 反过来下线 sale 什么也证明不了：它的 handler id 早已被 purchase 顶掉，
	// 闸 2 回查树时就已经放弃了，闸 3 根本走不到 —— 实测把闸 3 整个删掉，
	// 按 sale 那个方向写的测试仍然全绿。
	r.dropProxyRoutes("purchase")

	if !hasRoute(r, path) {
		t.Error("sale 还在广告这条路径，却被 purchase 的下线连累删掉了 —— " +
			"树里同路径只有一条路由，删了要等下一轮 refresh（最多 60s）才补回来")
	}
}

// TestReclaim_ReconcileDropsAbsentServices 锁定 refresh 的对账：注册中心列表里没有
// 的服务，其代理路由要收回。这是漏掉 watch 事件时的兜底。
func TestReclaim_ReconcileDropsAbsentServices(t *testing.T) {
	r := newReclaimRouter(t)

	r.store([]*registry.Service{remoteService("sale", "/sale/x")})
	r.store([]*registry.Service{remoteService("purchase", "/purchase/x")})

	// 列表里只剩 purchase
	r.reconcileProxyRoutes([]*registry.Service{{Name: "purchase"}})

	if hasRoute(r, "/sale/x") {
		t.Error("sale 已不在注册中心列表里，代理路由仍在")
	}
	if !hasRoute(r, "/purchase/x") {
		t.Error("还在列表里的 purchase 被误删")
	}
}

// TestReclaim_RebuildsAfterErroneousDrop 锁定误删是自愈的：下一轮 store() 会把它建
// 回来。这正是"宁可多跑一轮也不留坏路由"这个取舍成立的前提。
func TestReclaim_RebuildsAfterErroneousDrop(t *testing.T) {
	r := newReclaimRouter(t)

	svc := remoteService("sale", "/sale/x")
	r.store([]*registry.Service{svc})
	r.dropProxyRoutes("sale")
	if hasRoute(r, "/sale/x") {
		t.Fatal("测试前提不成立：应该已被删掉")
	}

	r.store([]*registry.Service{svc})
	if !hasRoute(r, "/sale/x") {
		t.Error("下一轮 store 没能把路由重建起来，误删就不是自愈的了")
	}
}

// TestReclaim_RepeatedStoreIsStable 锁定重复 store（refresh 每 60s 一轮）不会把路由
// 越记越多，也不会把自己刚建的删掉。
func TestReclaim_RepeatedStoreIsStable(t *testing.T) {
	r := newReclaimRouter(t)

	svc := remoteService("sale", "/sale/a", "/sale/b")
	for i := 0; i < 5; i++ {
		r.store([]*registry.Service{svc})
		if !hasRoute(r, "/sale/a") || !hasRoute(r, "/sale/b") {
			t.Fatalf("第 %d 轮之后路由没了", i+1)
		}
	}

	v, ok := r.proxyRoutes.Load("sale")
	if !ok {
		t.Fatal("记账丢了")
	}
	if got := len(v.(map[proxyRouteKey]int)); got != 2 {
		t.Errorf("记账条数 %d，应为 2 —— 重复 store 把账记重了", got)
	}
}

// TestReclaim_ServiceBecomesRpcOnly 锁定"这一轮一条都没建"也是有效信息。
//
// 服务还在注册中心里，但这一轮它只剩 RPC 节点（filteHttpNodes 把 HTTP 节点滤光了），
// 于是一条 HTTP 代理路由都建不出来。那说明先前建的都该收回，不能因为"本轮没数据"
// 就跳过记账 —— 跳过的话，旧路由会一直留着，正是 store() 只增不减的老毛病。
func TestReclaim_ServiceBecomesRpcOnly(t *testing.T) {
	r := newReclaimRouter(t)

	r.store([]*registry.Service{remoteService("sale", "/sale/x")})
	if !hasRoute(r, "/sale/x") {
		t.Fatal("测试前提不成立")
	}

	r.store([]*registry.Service{{
		Name:    "sale",
		Version: "latest",
		Nodes: []*registry.Node{{
			Id:       "sale-rpc",
			Address:  "192.0.2.10:45999",
			Metadata: map[string]string{"protocol": "TCP"},
		}},
		Endpoints: []*registry.Endpoint{
			{Name: "sale/sale/x", Method: []string{"GET"}, Path: "/sale/x"},
		},
	}})

	if hasRoute(r, "/sale/x") {
		t.Error("服务只剩 RPC 节点，HTTP 代理路由却还留着")
	}
	if _, ok := r.proxyRoutes.Load("sale"); ok {
		t.Error("本轮一条都没建，记账应清空")
	}
}
