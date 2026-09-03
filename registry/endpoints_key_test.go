package registry

import "testing"

// EndpointsKey 是"同一个服务名下的两个进程该不该被并成一条"的判据。
// 它错了的后果不是报错，是请求被轮询到不实现该端点的进程上，回一句 NotFound
// —— 与"这个部署没装那个模块"长得一模一样。
func TestEndpointsKey(t *testing.T) {
	eps := func(names ...string) []*Endpoint {
		out := make([]*Endpoint, 0, len(names))
		for _, n := range names {
			out = append(out, &Endpoint{Name: n})
		}
		return out
	}

	// 端点集不同 → 指纹不同 → 分成两条 Service，各自带自己的节点。
	a := EndpointsKey(eps("/a.read", "/a.write"))
	b := EndpointsKey(eps("/a.read"))
	if a == b {
		t.Fatalf("端点集不同却算出同一个指纹: %s", a)
	}

	// ★ 顺序无关。注册项里端点的顺序来自各自的路由注册序，同一份二进制两次启动
	// 都可能不同；不排序的话同一个端点集会算出不同指纹，每个进程各自成一组。
	if EndpointsKey(eps("/a.write", "/a.read")) != a {
		t.Fatal("端点顺序变了指纹就变，同一份二进制的两个副本会被拆开")
	}

	// 分隔符：没有它 "ab"+"c" 与 "a"+"bc" 会撞成同一个指纹。
	if EndpointsKey(eps("ab", "c")) == EndpointsKey(eps("a", "bc")) {
		t.Fatal("拼接没有分隔符，不同的端点集撞了指纹")
	}

	// 空集要有稳定值：没上报 endpoints 的老服务全部归到同一组。
	if EndpointsKey(nil) != EndpointsKey([]*Endpoint{}) {
		t.Fatal("空端点集的指纹不稳定")
	}

	// nil 元素不该让它 panic（注册项是网络来的，什么形状都可能）。
	_ = EndpointsKey([]*Endpoint{nil, {Name: "/a.read"}})
}
