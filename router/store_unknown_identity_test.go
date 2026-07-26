package router

import (
	"testing"

	"github.com/volts-dev/volts/registry"
)

// store() 在「识别不出自身」时必须一律不建代理路由。
//
// 背景（2026-07-26 真栈实测撞出）：此前这里分两支 —— 已挂本地路由则跳过，
// **尚无本地路由则放行全部节点**，理由写的是「纯网关刚启动，无自身可环」。
// 这个前提不成立：进程把自己的服务写进注册中心、RegistryCacher.LocalServices()
// 变得可见、模块把本地路由挂上路由树，这三件事并不同步。refresh() 首轮只延迟 5s，
// 完全可能落在「自己的服务已经在注册中心里、但 LocalServices() 还是空、路由树也
// 还没挂本地路由」的窗口里，于是本进程的服务被当成远程服务，按它的 endpoints
// 建出一整批**指向自身**的代理路由。
//
// 后果是永久性的：TTree.addNode 同路径后写覆盖，这些自环路由会顶掉随后挂上来的
// 本地 handler；而 refresh() 后续轮次会把该服务判成 local 直接跳过
// （isLocalService），再也不会回来修正已经写进树里的坏路由。
// 真栈症状：kylin 同进程托管 account，`/account/static/*` 对存在的文件稳定 508
// （`proxy self-loop blocked ... == own listen`），装模块取不到 Data 文件，
// 依赖闭包含该服务的租户一律建不出来。
func TestStore_NoProxyRouteWhenIdentityUnknown(t *testing.T) {
	cacher := registry.NewCacher(&echoRegistry{})
	r := New(WithRegistry(cacher))
	defer r.Close()

	// **刻意不注册任何本地服务** → localIdentity() 返回 nil，
	// 且此时路由树还是空的（正是出问题的那个启动窗口）。
	if got := len(cacher.LocalServices()); got != 0 {
		t.Fatalf("前置条件不成立：本地服务应为空，实际 %d", got)
	}
	if got := r.tree.Count.Load(); got != 0 {
		t.Fatalf("前置条件不成立：路由树应为空，实际 %d", got)
	}

	// 注册中心里“发现”到的服务，其节点地址其实就是本进程 —— 这正是竞态窗口里
	// 看到的形态（自己的注册已经写进去了，但 LocalServices() 还没反映出来）。
	selfAsRemote := &registry.Service{
		Name:    "account",
		Version: "latest",
		Nodes:   []*registry.Node{{Id: "gw-http", Address: "127.0.0.1:35999"}},
		Endpoints: []*registry.Endpoint{
			{Name: "account.static", Method: []string{"GET"}, Path: "/account/static/data/x.xml"},
		},
	}

	r.store([]*registry.Service{selfAsRemote})

	if route, _ := r.tree.Match("GET", "/account/static/data/x.xml"); route != nil {
		t.Fatal("识别不出自身时建出了代理路由 —— 它会指向本进程自己，" +
			"并永久顶掉随后挂上来的本地静态路由（真栈表现为该路径恒 508）")
	}
}

// 反向用例：自身身份已知时，真正的远程服务照常建代理路由 —— 上面的护盾不能
// 把正常功能一起挡掉。
func TestStore_BuildsProxyRouteWhenIdentityKnown(t *testing.T) {
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

	remote := &registry.Service{
		Name:    "account",
		Version: "latest",
		Nodes:   []*registry.Node{{Id: "acct-remote", Address: "192.0.2.10:35999"}},
		Endpoints: []*registry.Endpoint{
			{Name: "account.static", Method: []string{"GET"}, Path: "/account/static/data/x.xml"},
		},
	}
	r.store([]*registry.Service{remote})

	if route, _ := r.tree.Match("GET", "/account/static/data/x.xml"); route == nil {
		t.Fatal("自身身份已知、目标是真远端时，代理路由必须照常建出来")
	}
}

// 自身身份已知、但发现到的节点就是自己 → filteSelf 把节点清空 → 不建路由。
// 这条锁住「护盾生效后仍要靠 filteSelf 兜住同进程托管」的常规路径。
func TestStore_NoProxyRouteForSelfHostedService(t *testing.T) {
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

	// 同进程托管的另一个模块：服务名不同，节点却是同一个（id 与地址都相同）。
	// 这正是 kylin 这类集成二进制的真实形态。
	selfHosted := &registry.Service{
		Name:    "account",
		Version: "latest",
		Nodes:   []*registry.Node{{Id: "gw-http", Address: "127.0.0.1:35999"}},
		Endpoints: []*registry.Endpoint{
			{Name: "account.static", Method: []string{"GET"}, Path: "/account/static/data/y.xml"},
		},
	}
	r.store([]*registry.Service{selfHosted})

	if route, _ := r.tree.Match("GET", "/account/static/data/y.xml"); route != nil {
		t.Fatal("同进程托管的服务建出了代理路由 —— 会自环并顶掉本地路由")
	}
}
