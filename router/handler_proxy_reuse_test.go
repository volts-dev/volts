package router

import (
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/volts-dev/volts/transport"
)

// 网关反代路由在 registry 刷新后不得重复叠加中间件。
//
// generateHandler 的 ProxyHandler 分支会按 (service, path) 复用已有 id。
// 它一度用 Get(uid) 判断"是否已存在" —— 而 Get 会**从对象池里弹出一个已 init
// 完成的实例**（funcs 已含全局链与主处理器），再 Store 回去就把原型污染成成品。
// clone 不拷贝 inited，于是后续克隆的 inited 为 false，init 在已完整的 funcs 上
// 再前置一次全局链：网关路由上的中间件执行两次，且每 60s registry 刷新可能再叠一层。
//
// router.store() 在每次 registry watch 事件与 60s 周期刷新时都会重跑，
// 所以这条路径在网关上是常态而非边角。
func TestProxyHandlerSurvivesRegistryRefresh(t *testing.T) {
	r := New()
	defer r.Close()

	var midRuns atomic.Int64
	r.Use(func(ctx IContext) { midRuns.Add(1); ctx.Next() })

	url := &TUrl{Path: "/gw"}
	fn := func(ctx *THttpContext) {} // 同一函数值 → 同一 id，命中复用分支

	var firstLen int

	// 模拟：注册 → 服务若干请求 → registry 刷新重新 generateHandler，如此多轮
	for round := 0; round < 5; round++ {
		h := generateHandler(ProxyHandler, HttpHandler, []any{fn}, nil, url, "svc")

		// 模拟一次请求的完整生命周期（走生产路径：Get→clone→init→reset→Put）
		inst := defaultHandlerManager.Get(h.Id)
		if inst == nil {
			t.Fatalf("round %d: Get 返回 nil", round)
		}
		if !inst.inited {
			inst.init(r)
		}

		if round == 0 {
			firstLen = len(inst.funcs)
			if firstLen == 0 {
				t.Fatal("首轮 funcs 为空，测试没测到东西")
			}
		} else if len(inst.funcs) != firstLen {
			t.Fatalf("round %d: funcs = %d，首轮是 %d —— 中间件被重复叠加",
				round, len(inst.funcs), firstLen)
		}

		inst.reset()
		defaultHandlerManager.Put(h.Id, inst)
	}
}

// 端到端：全局中间件每个请求只跑一次（本地 handler 路径）。
func TestGlobalMiddlewareRunsExactlyOncePerRequest(t *testing.T) {
	var mid, main atomic.Int64

	r := New()
	defer r.Close()

	r.Use(func(ctx IContext) { mid.Add(1); ctx.Next() })
	r.Url([]string{"GET"}, "/once", func(ctx *THttpContext) { main.Add(1) })

	const n = 50
	for i := 0; i < n; i++ {
		req := httptest.NewRequest("GET", "/once", nil)
		r.ServeHTTP(httptest.NewRecorder(), transport.NewHttpRequest(req))
	}

	if got := mid.Load(); got != n {
		t.Errorf("全局中间件执行 %d 次，want %d（多出来的是重复执行）", got, n)
	}
	if got := main.Load(); got != n {
		t.Errorf("主控制器执行 %d 次，want %d", got, n)
	}
}
