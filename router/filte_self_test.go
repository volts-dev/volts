package router

import (
	"sync"
	"testing"

	"github.com/volts-dev/volts/internal/addr"
	"github.com/volts-dev/volts/registry"
)

// filteSelf 必须按 node.Id 剔除自身节点（原有行为）。
func TestFilteSelf_DropsById(t *testing.T) {
	me := &selfIdentity{
		ids:   map[string]struct{}{"gw-node-1": {}},
		addrs: map[string]struct{}{},
	}
	srv := &registry.Service{Name: "registry", Nodes: []*registry.Node{
		{Id: "gw-node-1", Address: "10.0.0.9:1234"}, // id 命中 → 剔除
		{Id: "remote-2", Address: "192.0.2.7:35999"}, // 远端 → 保留
	}}

	got := (&TRouter{}).filteSelf(srv, me)
	if len(got.Nodes) != 1 || got.Nodes[0].Id != "remote-2" {
		t.Fatalf("按 id 剔除失败，剩余 = %+v", got.Nodes)
	}
}

// 核心：id 对不上但地址落在本机监听端口上，必须靠地址线剔除。
// 这正是多进程 server.Debug() 撞端口时，纯 node.Id 匹配结构性漏掉的自环源。
func TestFilteSelf_DropsByAddress_DifferentId(t *testing.T) {
	me := &selfIdentity{
		ids:   map[string]struct{}{"gw-node-1": {}},
		addrs: map[string]struct{}{addr.LocalFormat("127.0.0.1:35999"): {}},
	}
	srv := &registry.Service{Name: "registry", Nodes: []*registry.Node{
		{Id: "sale-node-2", Address: "127.0.0.1:35999"}, // id 不同、地址即本机端口 → 必须剔除
		{Id: "sale-node-3", Address: "localhost:35999"}, // 归一化后同为 127.0.0.1:35999 → 剔除
		{Id: "sale-node-4", Address: "0.0.0.0:35999"},   // 通配符归一化到 loopback → 剔除
		{Id: "remote-5", Address: "192.0.2.7:35999"},    // 远端同端口 → 不算自环，保留
	}}

	got := (&TRouter{}).filteSelf(srv, me)
	if len(got.Nodes) != 1 || got.Nodes[0].Id != "remote-5" {
		t.Fatalf("按地址剔除失败（撞端口自环没挡住），剩余 = %+v", got.Nodes)
	}
}

// 远端节点一律保留，不得误伤。
func TestFilteSelf_KeepsRemoteNodes(t *testing.T) {
	me := &selfIdentity{
		ids:   map[string]struct{}{"gw-node-1": {}},
		addrs: map[string]struct{}{addr.LocalFormat("127.0.0.1:35999"): {}},
	}
	srv := &registry.Service{Name: "sale", Nodes: []*registry.Node{
		{Id: "sale-a", Address: "192.0.2.10:35999"},
		{Id: "sale-b", Address: "198.51.100.4:8080"},
	}}

	got := (&TRouter{}).filteSelf(srv, me)
	if len(got.Nodes) != 2 {
		t.Fatalf("远端节点被误剔，剩余 = %+v", got.Nodes)
	}
}

// filteSelf 返回副本，不得改写缓存里的原始 service.Nodes。
func TestFilteSelf_DoesNotMutateOriginal(t *testing.T) {
	me := &selfIdentity{
		ids:   map[string]struct{}{"gw-node-1": {}},
		addrs: map[string]struct{}{},
	}
	orig := []*registry.Node{
		{Id: "gw-node-1", Address: "10.0.0.9:1234"},
		{Id: "remote-2", Address: "192.0.2.7:35999"},
	}
	srv := &registry.Service{Name: "registry", Nodes: orig}

	(&TRouter{}).filteSelf(srv, me)

	if len(srv.Nodes) != 2 {
		t.Fatalf("原始 service.Nodes 被改写，len = %d", len(srv.Nodes))
	}
}

// echoRegistry：把注册进来的 service 原样（含 nodes）从 GetService 回吐，
// 让 registryCacher.LocalServices() 能被真实填充。
type echoRegistry struct {
	registry.IRegistry
	mu   sync.Mutex
	svcs map[string]*registry.Service
}

func (r *echoRegistry) Register(s *registry.Service, _ ...registry.Option) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.svcs == nil {
		r.svcs = map[string]*registry.Service{}
	}
	r.svcs[s.Name] = s
	return nil
}

func (r *echoRegistry) GetService(name string) ([]*registry.Service, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.svcs[name]; ok {
		return []*registry.Service{s}, nil
	}
	return nil, registry.ErrNotFound
}

func (r *echoRegistry) String() string { return "echo-registry" }

// localIdentity 应从 LocalServices() 同时收集 node.Id 与归一化地址；空时返回 nil。
func TestLocalIdentity_CollectsIdsAndAddrs(t *testing.T) {
	cacher := registry.NewCacher(&echoRegistry{})
	r := &TRouter{config: &Config{RegistryCacher: cacher}}

	// 尚未注册任何本地服务 → 返回 nil（自身未知）。
	if me := r.localIdentity(); me != nil {
		t.Fatalf("空 LocalServices 应返回 nil，得到 %+v", me)
	}

	if err := cacher.Register(&registry.Service{
		Name:  "registry",
		Nodes: []*registry.Node{{Id: "gw-http", Address: "127.0.0.1:35999"}},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	me := r.localIdentity()
	if me == nil {
		t.Fatal("注册后 localIdentity 不应为 nil")
	}
	if _, ok := me.ids["gw-http"]; !ok {
		t.Errorf("未收集 node.Id，ids = %+v", me.ids)
	}
	if _, ok := me.addrs[addr.LocalFormat("127.0.0.1:35999")]; !ok {
		t.Errorf("未收集归一化地址，addrs = %+v", me.addrs)
	}
}
