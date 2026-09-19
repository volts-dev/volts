package router

import (
	"strings"
	"testing"

	"github.com/volts-dev/volts/registry"
)

// 2026-09-19 真栈：connect_components 登记了只给自己出口主机用的根级兜底 `/*rootref`，
// 网关据此建出代理路由，与 website 的 `/*` 并列在根上。匹配只取先插入的那个 —— 所有
// CMS 页面经网关都被送去 connect_components 回 404，直连 websvc 一切正常，网关日志
// 一个字都没有。两条都钉：撞车要被点名；兜底路由有办法不登记进注册中心。

func catchAllSvc(name, addr, path string) *registry.Service {
	return &registry.Service{
		Name:      name,
		Version:   "latest",
		Nodes:     []*registry.Node{{Id: name + "-remote", Address: addr, Metadata: map[string]string{"protocol": "http"}}},
		Endpoints: []*registry.Endpoint{{Name: path, Method: []string{"GET"}, Path: path}},
	}
}

func TestStore_SiblingCatchAllsAreReported(t *testing.T) {
	var got []string
	prev := onCatchAllRival
	onCatchAllRival = func(existing, incoming *route) {
		got = append(got, routeOwner(incoming)+" vs "+routeOwner(existing))
	}
	t.Cleanup(func() { onCatchAllRival = prev })

	r := newReclaimRouter(t)
	r.store([]*registry.Service{catchAllSvc("connect_components", "192.0.2.1:38899", "/*rootref")})
	r.store([]*registry.Service{catchAllSvc("website", "192.0.2.2:36399", "/*")})

	if len(got) == 0 {
		t.Fatal("两条根级通配并列而没有任何报告：后到的那条永远到不了，且谁先谁后取决于服务发现顺序")
	}
	if !strings.Contains(got[0], "website") || !strings.Contains(got[0], "connect_components") {
		t.Errorf("报告要点名双方的服务，得到 %q", got[0])
	}

	// 同一条路由在下一轮 refresh 里再挂一次是同一分支，不是撞车，不许刷屏。
	n := len(got)
	r.store([]*registry.Service{catchAllSvc("website", "192.0.2.2:36399", "/*")})
	if len(got) != n {
		t.Errorf("重复 store 同一条通配又报了一次（%d → %d）：refresh 每 60s 一轮，这会变成刷屏", n, len(got))
	}
}

func TestEndpoints_SkipLocalOnlyRoutes(t *testing.T) {
	r := New()
	t.Cleanup(r.Close)
	grp := NewGroup(WithAloneService(true, "cc"))
	grp.Url([]string{"GET"}, "/*rootref", func(ctx *THttpContext) {}).WithOp(OpLocalOnly())
	grp.Url([]string{"GET"}, "/cc/browse", func(ctx *THttpContext) {})
	r.RegisterGroup(grp)

	var paths []string
	for _, eps := range r.Endpoints() {
		for _, e := range eps {
			paths = append(paths, e.Path)
		}
	}
	joined := strings.Join(paths, " ")
	if strings.Contains(joined, "rootref") {
		t.Errorf("OpLocalOnly 的路由仍被登记进注册中心：%s", joined)
	}
	if !strings.Contains(joined, "/cc/browse") {
		t.Errorf("普通路由不该受影响：%s", joined)
	}
	// 不登记不等于不生效：本进程自己的监听照样匹配得到它。
	if route, _ := r.tree.Match("GET", "/anything"); route == nil || route.Path() != "/*rootref" {
		t.Error("OpLocalOnly 的路由在本进程里也匹配不到了 —— 它只该退出注册中心，不该退出路由树")
	}
}
