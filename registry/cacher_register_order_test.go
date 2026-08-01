package registry

import (
	"errors"
	"testing"
)

// hookRegistry 在 Register 落盘的那一刻回调 —— 用来站在"注册中心事件已经发出、
// Register 还没返回"的时间点上观察 LocalServices()。真实环境里坐在这个位置的是
// 本进程自己的 watcher：etcd 一写入它立刻收到事件并驱动 router.store()。
type hookRegistry struct {
	IRegistry
	onRegister func()
	err        error
	svc        *Service
}

func (r *hookRegistry) Register(s *Service, opts ...Option) error {
	r.svc = s
	if r.onRegister != nil {
		r.onRegister()
	}
	return r.err
}

func (r *hookRegistry) GetService(name string) ([]*Service, error) {
	if r.svc == nil || r.svc.Name != name {
		return nil, ErrNotFound
	}
	// 注册中心按服务名合并：同名服务下还挂着**别的进程**的节点。
	return []*Service{{
		Name:    r.svc.Name,
		Version: r.svc.Version,
		Nodes: append(append([]*Node{}, r.svc.Nodes...),
			&Node{Id: "someone-else", Address: "192.0.2.10:35999"}),
	}}, nil
}

func (r *hookRegistry) Deregister(s *Service, opts ...Option) error { return nil }
func (r *hookRegistry) String() string                             { return "hook" }

// 本地身份必须在写注册中心**之前**登记好。
//
// 顺序反了会开一个真实的窗口：注册中心一落盘，本进程 watcher 立刻收到自己的创建
// 事件并驱动 router.store()，而 LocalServices() 里还没有这条记录 —— 自己的节点被
// 当成远端，按它的端点建出一整批指向本进程监听端口的代理路由。TTree 同路径后写
// 覆盖，本地 handler 当场被顶掉；refresh() 之后把该服务判成 local 直接跳过，坏路由
// 再没人纠正。真栈表现：`/{service}/static/*` 恒 508，模块升级取不到数据文件。
func TestCacherRegister_TracksIdentityBeforePublishing(t *testing.T) {
	var seen int
	hook := &hookRegistry{}
	c := NewCacher(hook)

	svc := &Service{
		Name:    "gamification",
		Version: "latest",
		Nodes:   []*Node{{Id: "http-self", Address: "127.0.0.1:35999"}},
	}

	hook.onRegister = func() { seen = len(c.LocalServices()) }

	if err := c.Register(svc); err != nil {
		t.Fatalf("register: %v", err)
	}

	if seen == 0 {
		t.Fatal("注册中心事件已经发出，LocalServices() 还是空的 —— " +
			"这一瞬间 router.store() 认不出自己，会给自己建出自环代理路由并顶掉本地路由")
	}
}

// 注册失败必须回滚，不能留下一个"其实没注册成功"的自己。
func TestCacherRegister_RollsBackOnFailure(t *testing.T) {
	hook := &hookRegistry{err: errors.New("etcd down")}
	c := NewCacher(hook)

	err := c.Register(&Service{
		Name:    "gamification",
		Version: "latest",
		Nodes:   []*Node{{Id: "http-self", Address: "127.0.0.1:35999"}},
	})
	if err == nil {
		t.Fatal("注册失败却返回了 nil")
	}

	if got := len(c.LocalServices()); got != 0 {
		t.Errorf("注册失败后本地服务未回滚，实际 %d 条", got)
	}
}

// LocalServices() 只能是"本进程注册了什么"，不能带上注册中心里同名服务的其它进程节点。
// 带上了的话 filteSelf 会把真正的远端节点当成自己剔掉，那个服务就永远没有代理路由 ——
// 与自环同样不可恢复，只是方向相反（恒 404 而不是恒 508）。
func TestCacherRegister_LocalServicesExcludeForeignNodes(t *testing.T) {
	hook := &hookRegistry{}
	c := NewCacher(hook)

	if err := c.Register(&Service{
		Name:    "gamification",
		Version: "latest",
		Nodes:   []*Node{{Id: "http-self", Address: "127.0.0.1:35999"}},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	locals := c.LocalServices()
	if len(locals) != 1 {
		t.Fatalf("本地服务应为 1 条，实际 %d", len(locals))
	}
	for _, n := range locals[0].Nodes {
		if n.Id != "http-self" {
			t.Errorf("LocalServices() 里混进了别的进程的节点 %s@%s", n.Id, n.Address)
		}
	}
}

// 续租（每 30s 一次的重复 Register）不得让 localServices 无限增长。
func TestCacherRegister_RenewalDoesNotDuplicate(t *testing.T) {
	hook := &hookRegistry{}
	c := NewCacher(hook)

	svc := &Service{
		Name:    "gamification",
		Version: "latest",
		Nodes:   []*Node{{Id: "http-self", Address: "127.0.0.1:35999"}},
	}
	for i := 0; i < 3; i++ {
		if err := c.Register(svc); err != nil {
			t.Fatalf("register #%d: %v", i, err)
		}
	}

	if got := len(c.LocalServices()); got != 1 {
		t.Errorf("续租重复注册后本地服务应仍为 1 条，实际 %d", got)
	}
}
