package router

import (
	"testing"

	"github.com/volts-dev/volts/registry"
)

// stripFromTree 模拟"树上的代理路由被别处拿掉了而记账还在"——指纹跳过唯一会放大的风险。
func stripFromTree(t *testing.T, r *TRouter, service, path string) {
	t.Helper()
	v, ok := r.proxyRoutes.Load(service)
	if !ok {
		t.Fatalf("%s 没有记账，测试前提不成立", service)
	}
	for key, id := range v.(map[proxyRouteKey]int) {
		if key.path == path {
			r.reclaimProxyRoute(service, key, id)
		}
	}
	if hasRoute(r, path) {
		t.Fatalf("%s 没剥掉，测试前提不成立", path)
	}
}

// 记录与身份都没变就不重建 —— 这条是整件事的目的：不托管 kylin 那 154 个服务的进程
// 每分钟白建几千条路由（connect_components 实测 ≈ 9.6MB/轮）。
//
// 断言方式是"被剥掉的路由没回来"：能证明 store() 真的没跑，也把代价写明了 —— 自愈
// 要等强制轮（force=true），见下一条。
func TestStoreIfChanged_SkipsUnchangedRecords(t *testing.T) {
	r := newReclaimRouter(t)
	svc := func() []*registry.Service {
		return []*registry.Service{remoteService("sale", "/sale/a", "/sale/b")}
	}

	r.storeIfChanged(svc(), false)
	if !hasRoute(r, "/sale/a") {
		t.Fatal("首轮没建路由，测试前提不成立")
	}
	stripFromTree(t, r, "sale", "/sale/a")

	r.storeIfChanged(svc(), false) // 同样的内容、新的对象（refresh 每轮拿到的都是副本）
	if hasRoute(r, "/sale/a") {
		t.Fatal("记录没变却重建了路由 —— 指纹没起作用，每轮仍是全量 store()")
	}

	r.storeIfChanged(svc(), true)
	if !hasRoute(r, "/sale/a") {
		t.Fatal("强制轮没有重建 —— 被误拿掉的代理路由再也补不回来")
	}
}

// 端点变了要重建：新路径建出来，砍掉的路径收回去。
func TestStoreIfChanged_RebuildsWhenEndpointsChange(t *testing.T) {
	r := newReclaimRouter(t)

	r.storeIfChanged([]*registry.Service{remoteService("sale", "/sale/keep", "/sale/gone")}, false)
	r.storeIfChanged([]*registry.Service{remoteService("sale", "/sale/keep", "/sale/new")}, false)

	if !hasRoute(r, "/sale/new") {
		t.Error("新端点没建路由 —— 指纹没认出端点变了")
	}
	if hasRoute(r, "/sale/gone") {
		t.Error("砍掉的端点还在树里")
	}
}

// 服务消失后回来，记录多半与消失前一模一样 —— 指纹必须随 dropProxyRoutes 一起清，
// 否则回来的服务被当成"没变"，那些路径一直 404。
func TestStoreIfChanged_RebuildsAfterServiceCameBack(t *testing.T) {
	r := newReclaimRouter(t)
	svc := func() []*registry.Service {
		return []*registry.Service{remoteService("sale", "/sale/a")}
	}

	r.storeIfChanged(svc(), false)
	r.dropProxyRoutes("sale")
	if hasRoute(r, "/sale/a") {
		t.Fatal("测试前提不成立")
	}

	r.storeIfChanged(svc(), false)
	if !hasRoute(r, "/sale/a") {
		t.Fatal("服务回来了，代理路由却没重建 —— 消失时没清指纹")
	}
}

// 一条路由都没建成的服务（这里：只有 RPC 端点）不在 proxyRoutes 里，reconcile 回收那一轮
// 碰不到它；它消失时指纹要单独清。
func TestReconcile_ClearsFingerprintOfServiceWithoutRoutes(t *testing.T) {
	r := newReclaimRouter(t)
	rpcOnly := &registry.Service{
		Name: "ledger", Version: "latest",
		Nodes:     []*registry.Node{{Id: "ledger-remote", Address: "192.0.2.11:46099"}},
		Endpoints: []*registry.Endpoint{{Name: "ledger.rpc", Method: []string{"CONNECT"}, Path: "/ledger/read"}},
	}

	r.storeIfChanged([]*registry.Service{rpcOnly}, false)
	if got := r.fingerprintedServices(); len(got) != 1 || got[0] != "ledger" {
		t.Fatalf("测试前提不成立：指纹表 %v", got)
	}

	r.reconcileProxyRoutes(nil) // 注册中心里已经没有它

	if got := r.fingerprintedServices(); len(got) != 0 {
		t.Fatalf("服务消失后指纹还留着: %v", got)
	}
}

// 认不出自己时 store() 整轮推迟，不能记指纹 —— 记了，下一轮能认出自己时会被当成"没变"
// 跳过，那一批路由就永远建不出来。
func TestStoreIfChanged_DeferredRecordsNoFingerprint(t *testing.T) {
	r := New(WithRegistry(registry.NewCacher(&echoRegistry{}))) // 没注册任何本地服务
	t.Cleanup(r.Close)

	if !r.storeIfChanged([]*registry.Service{remoteService("sale", "/sale/a")}, false) {
		t.Fatal("认不出自己时应回报 deferred")
	}
	if got := r.fingerprintedServices(); len(got) != 0 {
		t.Fatalf("推迟的一轮记下了指纹: %v", got)
	}
}

// 指纹要覆盖 store() 读到的每一个输入。漏一个就是"该重建的没重建"，而那不会报任何错。
func TestProxyFingerprint_CoversEveryStoreInput(t *testing.T) {
	base := func() ([]*registry.Service, *selfIdentity) {
		return []*registry.Service{{
				Name: "sale", Version: "latest",
				Nodes: []*registry.Node{{
					Id: "sale-1", Address: "192.0.2.10:36099",
					Metadata: map[string]string{"protocol": "http", "region": "a"},
				}},
				Endpoints: []*registry.Endpoint{
					{Method: []string{"GET"}, Path: "/sale/a"},
					{Method: []string{"GET", "POST"}, Path: "/sale/b"},
				},
			}}, &selfIdentity{
				ids:   map[string]struct{}{"gw-http": {}, "gw-rpc": {}},
				addrs: map[string]struct{}{"127.0.0.1:35999": {}, "127.0.0.1:45999": {}},
			}
	}
	s0, me0 := base()
	want := proxyFingerprint(s0, me0)

	if s, me := base(); proxyFingerprint(s, me) != want {
		t.Fatal("同样的内容算出了不同的指纹（map 遍历顺序不该有影响）—— 每轮都会误判成变了")
	}

	type svcs = []*registry.Service
	cases := map[string]func(s svcs, me *selfIdentity) svcs{
		"服务版本":  func(s svcs, _ *selfIdentity) svcs { s[0].Version = "2"; return s },
		"节点 id": func(s svcs, _ *selfIdentity) svcs { s[0].Nodes[0].Id = "sale-2"; return s },
		"节点地址":  func(s svcs, _ *selfIdentity) svcs { s[0].Nodes[0].Address = "192.0.2.10:36100"; return s },
		"节点协议":  func(s svcs, _ *selfIdentity) svcs { s[0].Nodes[0].Metadata["protocol"] = "rpc"; return s },
		"多一个节点": func(s svcs, _ *selfIdentity) svcs {
			s[0].Nodes = append(s[0].Nodes, &registry.Node{Id: "sale-2"})
			return s
		},
		"端点路径": func(s svcs, _ *selfIdentity) svcs { s[0].Endpoints[0].Path = "/sale/c"; return s },
		"端点方法": func(s svcs, _ *selfIdentity) svcs { s[0].Endpoints[1].Method = []string{"GET"}; return s },
		"多一个端点": func(s svcs, _ *selfIdentity) svcs {
			s[0].Endpoints = append(s[0].Endpoints, &registry.Endpoint{Path: "/x"})
			return s
		},
		"多一条同名记录":  func(s svcs, _ *selfIdentity) svcs { return append(s, &registry.Service{Name: "sale"}) },
		"本进程节点 id": func(s svcs, me *selfIdentity) svcs { me.ids["gw-new"] = struct{}{}; return s },
		"本进程地址":    func(s svcs, me *selfIdentity) svcs { delete(me.addrs, "127.0.0.1:45999"); return s },
		// 字段之间要有分隔：路径末尾挪一个字节给方法，拼起来的字节流不能一样。
		"路径与方法边界": func(s svcs, _ *selfIdentity) svcs {
			s[0].Endpoints[0].Path, s[0].Endpoints[0].Method = "/sale/aG", []string{"ET"}
			return s
		},
	}
	for name, mutate := range cases {
		s, me := base()
		s = mutate(s, me)
		if proxyFingerprint(s, me) == want {
			t.Errorf("%s 变了，指纹没变 —— 这类变化会被当成没变跳过", name)
		}
	}
}
