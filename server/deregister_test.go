package server

import (
	"testing"

	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/registry/memory"
	"github.com/volts-dev/volts/router"
)

type deregCtrl struct{}

func (deregCtrl) Ping(ctx *router.THttpContext) {}

// 一个服务器注册了多个服务（vectors 每个模块一个服务名）时，注销要把每一个都摘掉，
// 不能只摘 config.Name —— 否则其余记录要等租约过期，别的进程一直看得到已经停掉的节点。
func TestServer_DeregisterRemovesEveryRegisteredService(t *testing.T) {
	reg := memory.New()
	r := router.New()
	for _, name := range []string{"mod_a", "mod_b"} {
		g := router.NewGroup(router.WithAloneService(true, name))
		g.Url([]string{"GET"}, "/"+name+"/ping", deregCtrl.Ping)
		r.RegisterGroup(g)
	}
	srv := New(Name("deregsrv"), Address("127.0.0.1:0"), WithRegistry(reg), WithRouter(r))

	if err := srv.Register(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mod_a", "mod_b"} {
		if got, err := reg.GetService(name); err != nil || len(got) == 0 {
			t.Fatalf("注册后看不到 %s：%v", name, err)
		}
	}

	if err := srv.Deregister(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mod_a", "mod_b"} {
		got, err := reg.GetService(name)
		if err == nil && len(got) > 0 && len(got[0].Nodes) > 0 {
			t.Fatalf("注销后 %s 还在注册中心里：%d 个节点", name, len(got[0].Nodes))
		}
		if err != nil && err != registry.ErrNotFound {
			t.Fatalf("%s: %v", name, err)
		}
	}

	// 注销后再注册（RegisterCheck 失败后的恢复路径）照旧能摘干净。
	if err := srv.Register(); err != nil {
		t.Fatal(err)
	}
	if err := srv.Deregister(); err != nil {
		t.Fatal(err)
	}
	if got, err := reg.GetService("mod_b"); err == nil && len(got) > 0 {
		t.Fatalf("第二轮注销后 mod_b 还在")
	}
}
