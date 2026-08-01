package router

import (
	"testing"

	"github.com/volts-dev/volts/registry"
)

// 注册中心发现来的代理路由，绝不能顶掉本进程自己在服务的 HTTP 路由。
//
// 真栈事故（2026-08-01，kylin）：`/gamification/static/security/*.xml` 恒 508
// （`http: proxy self-loop blocked ... Target: http://127.0.0.1:35999 == own listen`），
// 而同一进程里 account/product 等模块的同形路径一切正常 —— 因为这是竞态，每次重启
// 命中的模块都不一样。后果不是慢一点：模块升级要经
// `GET /{service}/static/{file}` 取数据文件（core/download），取不到就整包失败，
// 报 "upgrade gamification: prefetch module data: ... Failed to download data file"。
//
// filteSelf 只在"认得出自己"时管用，而认自己依赖 RegistryCacher.LocalServices() 的
// 填充时机：注册中心先落盘、localServices 后更新（已另行修正顺序），且同进程的
// http/rpc 两个 server 各持一个 cacher，彼此的节点互相不可见。这条护盾与"认不认得出
// 自己"无关 —— 本进程能自己服务的路径，转发出去在任何拓扑下都不会更对。
func TestStore_DoesNotOverrideLocalHttpRoute(t *testing.T) {
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

	// 本地模块自己挂的静态路由。
	grp := NewGroup()
	grp.Url([]string{"GET"}, "/gamification/static/security/{:filepath}", func(ctx *THttpContext) {})
	r.RegisterGroup(grp)

	before, _ := r.tree.Match("GET", "/gamification/static/security/x.xml")
	if before == nil {
		t.Fatal("本地静态路由注册失败，测试前提不成立")
	}

	// 注册中心把同一条路径报成"远程服务的端点"——节点是别的进程，filteSelf 无从剔除。
	r.store([]*registry.Service{{
		Name:    "gamification",
		Version: "latest",
		Nodes:   []*registry.Node{{Id: "other-http", Address: "192.0.2.10:35999"}},
		Endpoints: []*registry.Endpoint{
			{Name: "gamification.static", Method: []string{"GET", "HEAD"},
				Path: "/gamification/static/security/{:filepath}"},
		},
	}})

	after, _ := r.tree.Match("GET", "/gamification/static/security/x.xml")
	if after == nil {
		t.Fatal("本地静态路由在 store 后消失了")
	}
	if after.Id() != before.Id() {
		t.Errorf("本地 HTTP handler 被代理路由顶掉：route id %v → %v —— "+
			"该路径此后要么自环 508、要么被转发到别的进程，模块升级取不到数据文件",
			before.Id(), after.Id())
	}
}

// 护盾不能误伤：本地没有的路径照常建代理路由。
func TestStore_StillBuildsProxyRouteForForeignPath(t *testing.T) {
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

	grp := NewGroup()
	grp.Url([]string{"GET"}, "/gamification/static/security/{:filepath}", func(ctx *THttpContext) {})
	r.RegisterGroup(grp)

	r.store([]*registry.Service{{
		Name:    "sale",
		Version: "latest",
		Nodes:   []*registry.Node{{Id: "sale-remote", Address: "192.0.2.10:36099"}},
		Endpoints: []*registry.Endpoint{
			{Name: "sale.static", Method: []string{"GET"}, Path: "/sale/static/data/{:filepath}"},
		},
	}})

	if route, _ := r.tree.Match("GET", "/sale/static/data/x.xml"); route == nil {
		t.Error("本地没有的路径应照常建出代理路由，护盾把正常功能一起挡掉了")
	}
}

// 只剩 RPC 节点的服务记录，不得建出 HTTP 代理路由。
//
// vectors 的 http/rpc 两个 server 共用一个 router，各自按自己的端口注册**同名服务**，
// 且两条记录带的是同一份完整端点表（HTTP 路由 + CONNECT 路由都在）。当 filteSelf 刚把
// HTTP 节点当自己剔掉、或缓存里那一版只收到 rpc 键的事件时，剩下的就是纯 RPC 节点 ——
// 老逻辑照样按表里的 HTTP 端点建路由，而请求真到来时 reverse_proxy.getService 只在
// protocol=http/https 的节点里选，选回来的正是本进程自己的 HTTP 端口：508 自环，
// 且本地路由已经被这条代理路由顶掉。
func TestStore_SkipsHttpRoutesForRpcOnlyNodes(t *testing.T) {
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

	r.store([]*registry.Service{{
		Name:    "gamification",
		Version: "latest",
		Nodes: []*registry.Node{{
			Id:       "rpc-node",
			Address:  "192.0.2.10:45999",
			Metadata: map[string]string{"protocol": "TCP"},
		}},
		Endpoints: []*registry.Endpoint{
			{Name: "gamification.static", Method: []string{"GET"},
				Path: "/gamification/static/data/{:filepath}"},
		},
	}})

	if route, _ := r.tree.Match("GET", "/gamification/static/data/x.xml"); route != nil {
		t.Error("只有 RPC 节点的服务建出了 HTTP 代理路由 —— 请求时 getService 只选 http 节点，" +
			"会选回本进程自己，形成 508 自环")
	}
}

// 反向用例：节点声明 protocol=http 时照常建路由；没有 metadata 的节点也保留
// （测试、老注册记录、别的注册中心后端）。
func TestStore_KeepsHttpAndUnlabelledNodes(t *testing.T) {
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

	r.store([]*registry.Service{{
		Name:    "sale",
		Version: "latest",
		Nodes: []*registry.Node{
			{Id: "sale-rpc", Address: "192.0.2.10:46099", Metadata: map[string]string{"protocol": "TCP"}},
			{Id: "sale-http", Address: "192.0.2.10:36099", Metadata: map[string]string{"protocol": "http"}},
		},
		Endpoints: []*registry.Endpoint{
			{Name: "sale.http", Method: []string{"POST"}, Path: "/sale/order/create"},
		},
	}, {
		Name:    "stock",
		Version: "latest",
		Nodes:   []*registry.Node{{Id: "stock-node", Address: "192.0.2.11:36099"}}, // 无 metadata
		Endpoints: []*registry.Endpoint{
			{Name: "stock.http", Method: []string{"POST"}, Path: "/stock/move/create"},
		},
	}})

	if route, _ := r.tree.Match("POST", "/sale/order/create"); route == nil {
		t.Error("服务同时挂着 http 与 rpc 节点时，HTTP 代理路由必须照常建出")
	}
	if route, _ := r.tree.Match("POST", "/stock/move/create"); route == nil {
		t.Error("没有 protocol metadata 的节点被误剔除了 —— 判定应取宽，只挡明确非 http 的")
	}
}
