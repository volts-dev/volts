package router

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/volts-dev/volts/transport"
)

// ------------------------------------------------------------
// 测试用中间件
// ------------------------------------------------------------

// markMid 是一个通用中间件（不声明 Transports），两侧都应装上。
type markMid struct {
	tag  string
	sink *string
}

func (m markMid) Name() string { return "mark-" + m.tag }

func (m markMid) Handler(ctx IContext) {
	*m.sink += m.tag
	ctx.Next()
}

// httpOnlyMid 声明只适用于 HTTP。
type httpOnlyMid struct {
	sink *string
}

func (m httpOnlyMid) Name() string { return "http-only" }

func (m httpOnlyMid) Handler(ctx IContext) {
	*m.sink += "H"
	ctx.Next()
}

func (m httpOnlyMid) Transports() []TransportType {
	return []TransportType{HttpHandler}
}

// ------------------------------------------------------------
// 路由级中间件
// ------------------------------------------------------------

// 路由级中间件：Url(methods, path, 主控制器, 中间件...) —— handlers[0] 是主控制器，
// handlers[1:] 才是中间件，且中间件先于主控制器执行。
//
// 这条断言存在的原因：参数顺序颠倒（把中间件放在 handlers[0]）不会报"参数用错"，
// 而是让中间件被当成控制器走进反射兜底路径，报一句无关的
// "reflect: call of reflect.Value.NumField on interface Value"，极易被误判成
// "这个中间件类型不被支持"。
func TestRouteLevelMiddlewareOrder(t *testing.T) {
	order := ""

	r := New()
	defer r.Close()

	// handlers[0]=主控制器，handlers[1:]=中间件
	r.Url([]string{"GET"}, "/probe",
		func(ctx *THttpContext) { order += "main" },
		func(ctx IContext) { order += "A>"; ctx.Next() },      // func(IContext)
		func(ctx *THttpContext) { order += "B>"; ctx.Next() }, // func(*THttpContext)
	)

	req := httptest.NewRequest("GET", "/probe", nil)
	r.ServeHTTP(httptest.NewRecorder(), transport.NewHttpRequest(req))

	if order != "A>B>main" {
		t.Fatalf("执行顺序 = %q, want %q", order, "A>B>main")
	}
}

// ------------------------------------------------------------
// 传输类型隔离
// ------------------------------------------------------------

// HTTP 专属中间件挂到 RPC 路由上必须被静默跳过，而不是 nil panic。
//
// 修复前：addMiddlewares 不看 TransportType，把 func(*THttpContext) 存成
// HttpFunc；invokeHandlers 走 RPC 分支去调 nil 的 RpcFunc → 每个请求 nil panic。
// 默认 Recover 开着，线上表现是"这条 RPC 路由恒定 500"，日志里完全看不出
// 是中间件挂错了边。
func TestHttpMiddlewareSkippedOnRpcRoute(t *testing.T) {
	ran := ""

	r := New()
	defer r.Close()

	rt := r.Url([]string{"CONNECT"}, "/rpc.skip",
		func(ctx *TRpcContext) { ran += "main" },
		func(ctx *THttpContext) { ran += "http"; ctx.Next() }, // 挂错边
	)

	ctx := NewRpcHandler(r)
	ctx.router = r
	ctx.route = rt
	ctx.inited = true

	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("HTTP 中间件挂到 RPC 路由触发 panic: %v", p)
		}
	}()

	for _, id := range rt.Handlers() {
		hd := defaultHandlerManager.Get(id)
		if !hd.inited {
			hd.init(r)
		}
		hd.Invoke(ctx)
	}

	if ran != "main" {
		t.Fatalf("执行结果 = %q, want %q（HTTP 中间件不应进入 RPC 链）", ran, "main")
	}
}

// 反向：RPC 专属中间件挂到 HTTP 路由上同样应被跳过。
func TestRpcMiddlewareSkippedOnHttpRoute(t *testing.T) {
	ran := ""

	r := New()
	defer r.Close()

	r.Url([]string{"GET"}, "/http.skip",
		func(ctx *THttpContext) { ran += "main" },
		func(ctx *TRpcContext) { ran += "rpc"; ctx.Next() }, // 挂错边
	)

	req := httptest.NewRequest("GET", "/http.skip", nil)
	r.ServeHTTP(httptest.NewRecorder(), transport.NewHttpRequest(req))

	if ran != "main" {
		t.Fatalf("执行结果 = %q, want %q（RPC 中间件不应进入 HTTP 链）", ran, "main")
	}
}

// ------------------------------------------------------------
// 全局链 Use()
// ------------------------------------------------------------

// Use 挂的中间件对所有路由生效，且排在路由级中间件之前。
func TestUseAppliesGloballyBeforeRouteLevel(t *testing.T) {
	order := ""

	r := New()
	defer r.Close()

	r.Use(func(ctx IContext) { order += "G>"; ctx.Next() })
	r.Url([]string{"GET"}, "/g1",
		func(ctx *THttpContext) { order += "main" },
		func(ctx IContext) { order += "R>"; ctx.Next() },
	)

	req := httptest.NewRequest("GET", "/g1", nil)
	r.ServeHTTP(httptest.NewRecorder(), transport.NewHttpRequest(req))

	if order != "G>R>main" {
		t.Fatalf("执行顺序 = %q, want %q（全局须早于路由级）", order, "G>R>main")
	}
}

// Use 写在 Url 之后依然生效 —— 中间件在 handler 首次初始化（首个请求）时才装配，
// 因此不存在 gin 那种"Use 必须先于路由注册"的顺序陷阱。
func TestUseWorksAfterRouteRegistration(t *testing.T) {
	order := ""

	r := New()
	defer r.Close()

	r.Url([]string{"GET"}, "/g2", func(ctx *THttpContext) { order += "main" })
	r.Use(func(ctx IContext) { order += "G>"; ctx.Next() }) // 注册路由之后才 Use

	req := httptest.NewRequest("GET", "/g2", nil)
	r.ServeHTTP(httptest.NewRecorder(), transport.NewHttpRequest(req))

	if order != "G>main" {
		t.Fatalf("执行顺序 = %q, want %q", order, "G>main")
	}
}

// 多次 Use 按调用顺序执行。
func TestUseOrderIsRegistrationOrder(t *testing.T) {
	order := ""

	r := New()
	defer r.Close()

	r.Use(func(ctx IContext) { order += "1"; ctx.Next() })
	r.Use(func(ctx IContext) { order += "2"; ctx.Next() })
	r.Url([]string{"GET"}, "/g3", func(ctx *THttpContext) { order += "m" })

	req := httptest.NewRequest("GET", "/g3", nil)
	r.ServeHTTP(httptest.NewRecorder(), transport.NewHttpRequest(req))

	if order != "12m" {
		t.Fatalf("执行顺序 = %q, want %q", order, "12m")
	}
}

// Use 的 HTTP 专属条目不得进入 RPC 链 —— 单入口能同时服务两种传输的前提。
func TestUseFiltersByTransport(t *testing.T) {
	httpRan := ""
	rpcRan := ""

	r := New()
	defer r.Close()

	// 三种声明方式各一条
	r.Use(
		func(ctx *THttpContext) { httpRan += "H"; ctx.Next() }, // 仅 HTTP
		func(ctx *TRpcContext) { rpcRan += "R"; ctx.Next() },   // 仅 RPC
		func(ctx IContext) { // 通用：两侧都应执行
			if _, ok := ctx.(*THttpContext); ok {
				httpRan += "C"
			} else {
				rpcRan += "C"
			}
			ctx.Next()
		},
	)

	r.Url([]string{"GET"}, "/mixed", func(ctx *THttpContext) { httpRan += "m" })
	rpcRoute := r.Url([]string{"CONNECT"}, "/mixed.rpc", func(ctx *TRpcContext) { rpcRan += "m" })

	// HTTP 侧
	req := httptest.NewRequest("GET", "/mixed", nil)
	r.ServeHTTP(httptest.NewRecorder(), transport.NewHttpRequest(req))

	if httpRan != "HCm" {
		t.Errorf("HTTP 链 = %q, want %q（应含 HTTP 专属 + 通用，不含 RPC 专属）", httpRan, "HCm")
	}

	// RPC 侧
	rctx := NewRpcHandler(r)
	rctx.router = r
	rctx.route = rpcRoute
	rctx.inited = true
	for _, id := range rpcRoute.Handlers() {
		hd := defaultHandlerManager.Get(id)
		if !hd.inited {
			hd.init(r)
		}
		hd.Invoke(rctx)
	}

	if rpcRan != "RCm" {
		t.Errorf("RPC 链 = %q, want %q（应含 RPC 专属 + 通用，不含 HTTP 专属）", rpcRan, "RCm")
	}
}

// 实现 IMiddlewareTransport 的 IMiddleware 按其声明筛选；
// 未实现的默认两侧都装。
func TestUseRespectsIMiddlewareTransport(t *testing.T) {
	httpRan := ""
	rpcRan := ""

	r := New()
	defer r.Close()

	r.Use(
		func(rt IRouter) IMiddleware { return httpOnlyMid{sink: &httpRan} },
		func(rt IRouter) IMiddleware { return markMid{tag: "*", sink: &httpRan} },
	)
	r.Url([]string{"GET"}, "/m1", func(ctx *THttpContext) { httpRan += "m" })

	req := httptest.NewRequest("GET", "/m1", nil)
	r.ServeHTTP(httptest.NewRecorder(), transport.NewHttpRequest(req))

	if httpRan != "H*m" {
		t.Fatalf("HTTP 链 = %q, want %q", httpRan, "H*m")
	}

	// RPC 侧：httpOnlyMid 应被摘除，markMid（未声明）保留
	r2 := New()
	defer r2.Close()
	r2.Use(
		func(rt IRouter) IMiddleware { return httpOnlyMid{sink: &rpcRan} },
		func(rt IRouter) IMiddleware { return markMid{tag: "*", sink: &rpcRan} },
	)
	rpcRoute := r2.Url([]string{"CONNECT"}, "/m1.rpc", func(ctx *TRpcContext) { rpcRan += "m" })

	rctx := NewRpcHandler(r2)
	rctx.router = r2
	rctx.route = rpcRoute
	rctx.inited = true
	for _, id := range rpcRoute.Handlers() {
		hd := defaultHandlerManager.Get(id)
		if !hd.inited {
			hd.init(r2)
		}
		hd.Invoke(rctx)
	}

	if strings.Contains(rpcRan, "H") {
		t.Errorf("RPC 链 = %q，声明了 HTTP-only 的中间件不应出现", rpcRan)
	}
	if rpcRan != "*m" {
		t.Errorf("RPC 链 = %q, want %q", rpcRan, "*m")
	}
}
