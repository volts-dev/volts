package registry

import "testing"

// Service.Equal 用于 TRouter.isLocalService 把「本进程自己托管的服务」排除在建代理
// 路由之外。它返回 true 就意味着 refresh() 直接跳过该服务，永远不为它建路由 ——
// 因此一次误判 true 的代价是「远程服务永久不可达」，方向上必须取严。
//
// 历史 bug：节点数不等时判断整个被 if 跳过，直接落到结尾的 return true；名称也完全
// 不参与比较。注册中心里存在僵尸节点（进程被 SIGKILL，lease 未撤销，同名服务下新旧
// 节点并存）时必然触发，表现为跨进程调用时好时坏。
func TestServiceEqual(t *testing.T) {
	node := func(ids ...string) []*Node {
		ns := make([]*Node, 0, len(ids))
		for _, id := range ids {
			ns = append(ns, &Node{Id: id})
		}
		return ns
	}

	cases := []struct {
		name string
		a, b *Service
		want bool
	}{
		{
			name: "同名同版本、节点集合一致",
			a:    &Service{Name: "product", Version: "latest", Nodes: node("http-A", "rpc-A")},
			b:    &Service{Name: "product", Version: "latest", Nodes: node("rpc-A", "http-A")}, // 顺序无关
			want: true,
		},
		{
			name: "★ 节点数不等必须为 false（旧实现在这里返回 true）",
			a:    &Service{Name: "product", Version: "latest", Nodes: node("http-A")},
			b:    &Service{Name: "product", Version: "latest", Nodes: node("http-A", "rpc-A")},
			want: false,
		},
		{
			name: "★ 僵尸节点导致远程服务节点数翻倍：不能判成本地服务",
			a:    &Service{Name: "sale", Version: "latest", Nodes: node("http-local", "rpc-local")},
			b:    &Service{Name: "sale", Version: "latest", Nodes: node("http-old", "rpc-old", "http-new", "rpc-new")},
			want: false,
		},
		{
			name: "★ 名称不同必须为 false（旧实现完全不比较名称）",
			a:    &Service{Name: "product", Version: "latest", Nodes: node("http-A")},
			b:    &Service{Name: "sale", Version: "latest", Nodes: node("http-B")},
			want: false,
		},
		{
			name: "版本不同",
			a:    &Service{Name: "product", Version: "v1", Nodes: node("http-A")},
			b:    &Service{Name: "product", Version: "v2", Nodes: node("http-A")},
			want: false,
		},
		{
			name: "节点数相同但 id 不同（不同进程的同名服务）",
			a:    &Service{Name: "product", Version: "latest", Nodes: node("http-A", "rpc-A")},
			b:    &Service{Name: "product", Version: "latest", Nodes: node("http-B", "rpc-B")},
			want: false,
		},
		{
			name: "两边都无节点",
			a:    &Service{Name: "product", Version: "latest"},
			b:    &Service{Name: "product", Version: "latest"},
			want: true,
		},
	}

	for _, c := range cases {
		if got := c.a.Equal(c.b); got != c.want {
			t.Errorf("%s: Equal = %v, want %v", c.name, got, c.want)
		}
	}
}

// noop registry 会返回 nil service，Equal 不得 panic。
func TestServiceEqualNil(t *testing.T) {
	srv := &Service{Name: "product", Nodes: []*Node{{Id: "http-A"}}}
	var nilSrv *Service

	if srv.Equal(nil) {
		t.Error("非空服务与 nil 不应相等")
	}
	if nilSrv.Equal(srv) {
		t.Error("nil 与非空服务不应相等")
	}
	if !nilSrv.Equal(nil) {
		t.Error("nil 与 nil 应相等")
	}
}
