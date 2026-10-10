package registry

import "testing"

type readBackCounter struct {
	hookRegistry
	registers, gets int
}

func (r *readBackCounter) Register(s *Service, opts ...Option) error {
	r.registers++
	return r.hookRegistry.Register(s, opts...)
}

func (r *readBackCounter) GetService(name string) ([]*Service, error) {
	r.gets++
	return r.hookRegistry.GetService(name)
}

// 续租时身份（名字 / 版本 / 节点 id 与地址）没变就只续租、不回读。
//
// 此前每轮每个服务都 GetService 一次：前缀读 + 整条 JSON 解码 + 逐条验签。kylin
// 约 120 个服务、30 秒一轮，空闲时 etcd 每分钟千余次 Range、发出 150MB，占 kylin
// 空闲 CPU 的三分之一（2026-10-10 实测）。
func TestCacherRegister_RenewalSkipsReadBack(t *testing.T) {
	r := &readBackCounter{}
	c := NewCacher(r)
	svc := func(addr string) *Service {
		return &Service{Name: "stock", Version: "latest", Nodes: []*Node{{Id: "http-self", Address: addr}}}
	}

	for i := 0; i < 5; i++ {
		if err := c.Register(svc("127.0.0.1:35999")); err != nil {
			t.Fatal(err)
		}
	}
	if r.registers != 5 || r.gets != 1 {
		t.Fatalf("5 次续租：Register %d 次、回读 %d 次，期望 5 / 1", r.registers, r.gets)
	}
	if ls := c.LocalServices(); len(ls) != 1 || len(ls[0].Nodes) != 1 || ls[0].Nodes[0].Id != "http-self" {
		t.Fatalf("本地身份应当是回读出来、只含本进程节点的那份: %+v", ls)
	}

	// 地址变了（换网）：必须重新回读，本地身份跟着纠正
	if err := c.Register(svc("10.0.0.5:35999")); err != nil {
		t.Fatal(err)
	}
	if r.gets != 2 {
		t.Fatalf("地址变了却没有回读（回读 %d 次）", r.gets)
	}
	if got := c.LocalServices()[0].Nodes[0].Address; got != "10.0.0.5:35999" {
		t.Fatalf("本地身份没跟上新地址: %s", got)
	}

	// 注销之后再注册：从头来
	c.Deregister(svc("10.0.0.5:35999"))
	if err := c.Register(svc("10.0.0.5:35999")); err != nil {
		t.Fatal(err)
	}
	if r.gets != 3 {
		t.Fatalf("注销后重新注册没有回读（回读 %d 次）", r.gets)
	}
}

// 回读没认出本进程节点时不记指纹，下一轮继续回读。
func TestCacherRegister_RetriesReadBackUntilSelfFound(t *testing.T) {
	r := &readBackCounter{}
	c := NewCacher(r)
	svc := &Service{Name: "stock", Version: "latest", Nodes: []*Node{{Id: "http-self", Address: "127.0.0.1:1"}}}

	r.svc = nil // GetService 回 ErrNotFound：还看不见自己
	r.hookRegistry.onRegister = func() { r.svc = nil }
	c.Register(svc)
	c.Register(svc)
	if r.gets != 2 {
		t.Fatalf("没回读成功也停止了回读（回读 %d 次）", r.gets)
	}
}
