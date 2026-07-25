package router

import (
	"testing"

	"github.com/volts-dev/volts/registry"
)

// store() 不得为 CONNECT(RPC) 端点建代理路由。
//
// RPC 层没有反向代理实现 —— RpcReverseProxy 曾是空函数体，请求命中即被静默吞掉，
// 调用方只能等满 RequestTimeout 拿到 "context deadline exceeded"。更糟的是
// TTree.addNode 对同路径路由是「后写覆盖」：这条空 handler 一旦建出来就会顶掉本地
// 真正的 RPC handler，把本来正常的本地调用也变成黑洞。跨进程 RPC 由 client 依据
// 注册中心直连目标节点，从不经网关转发，因此这里没有任何东西需要代理。
func TestStore_SkipsRpcProxyRoutes(t *testing.T) {
	cacher := registry.NewCacher(&echoRegistry{})
	r := New(WithRegistry(cacher))
	defer r.Close()

	// 本进程自身的注册记录 —— 让 localIdentity() 非 nil，store 才会走正常分支。
	if err := cacher.Register(&registry.Service{
		Name:    "registry",
		Version: "latest",
		Nodes:   []*registry.Node{{Id: "gw-http", Address: "127.0.0.1:35999"}},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	// 一个真正的远程服务，同时暴露 HTTP 与 RPC 端点。
	remote := &registry.Service{
		Name:    "sale",
		Version: "latest",
		Nodes:   []*registry.Node{{Id: "sale-remote", Address: "192.0.2.10:35999"}},
		Endpoints: []*registry.Endpoint{
			{Name: "sale.http", Method: []string{"POST"}, Path: "/sale/order/create"},
			{Name: "sale.rpc", Method: []string{"CONNECT"}, Path: "/sale/order/read"},
		},
	}

	r.store([]*registry.Service{remote})

	// HTTP 端点：正常建出代理路由。
	if route, _ := r.tree.Match("POST", "/sale/order/create"); route == nil {
		t.Error("远程服务的 HTTP 端点应建出代理路由，实际没有")
	}

	// RPC 端点：绝不能建。建了就是一个吞请求的黑洞。
	if route, _ := r.tree.Match("CONNECT", "/sale/order/read"); route != nil {
		t.Error("CONNECT 端点建出了代理路由 —— RPC 无反代实现，这条路由只会静默吞掉请求直到调用方超时")
	}
}

// 本地已注册的 RPC handler 不得被注册中心刷新建出的代理路由顶掉。
//
// 这是单进程托管多个服务时最致命的表现：本地 RPC 明明能处理，却被同路径的空代理
// 路由覆盖，所有调用静默挂到超时。
func TestStore_DoesNotOverrideLocalRpcHandler(t *testing.T) {
	cacher := registry.NewCacher(&echoRegistry{})
	r := New(WithRegistry(cacher))
	defer r.Close()

	if err := cacher.Register(&registry.Service{
		Name:    "registry",
		Version: "latest",
		Nodes:   []*registry.Node{{Id: "gw-http", Address: "127.0.0.1:35999"}},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	// 本地注册一个真正的 RPC handler。
	grp := NewGroup()
	grp.Url([]string{"CONNECT"}, "/sale/order/read", func(ctx *TRpcContext) {})
	r.RegisterGroup(grp)

	before, _ := r.tree.Match("CONNECT", "/sale/order/read")
	if before == nil {
		t.Fatal("本地 RPC 路由注册失败，测试前提不成立")
	}

	// 注册中心报告同路径的远程 RPC 端点 —— 不得覆盖本地 handler。
	r.store([]*registry.Service{{
		Name:    "sale",
		Version: "latest",
		Nodes:   []*registry.Node{{Id: "sale-remote", Address: "192.0.2.10:35999"}},
		Endpoints: []*registry.Endpoint{
			{Name: "sale.rpc", Method: []string{"CONNECT"}, Path: "/sale/order/read"},
		},
	}})

	after, _ := r.tree.Match("CONNECT", "/sale/order/read")
	if after == nil {
		t.Fatal("本地 RPC 路由在 store 后消失了")
	}
	if after.Id() != before.Id() {
		t.Errorf("本地 RPC handler 被代理路由顶掉：route id %v → %v", before.Id(), after.Id())
	}
}
