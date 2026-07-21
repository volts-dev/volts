package router

import (
	"net/http/httptest"
	"testing"

	"github.com/volts-dev/volts/transport"
)

// 请求热路径基准：一条路由挂 N 个中间件，量整个 ServeHTTP。
//
// 存在的理由：invokeHandlers 曾把 switch TransportType 与 ctx 类型断言放在
// 遍历 funcs 的循环体内，被改成"进循环前定型一次"。那次改动**没有实测**就
// 被称作优化 —— 而定型方案要给 runChain 传一个捕获了 ctx 的闭包，闭包会逃逸
// 到堆上，每请求多一次分配。省下的类型断言是否抵得过这次分配，只能量。
func benchServe(b *testing.B, midCount int) {
	r := New()
	defer r.Close()

	handlers := []any{func(ctx *THttpContext) { ctx.Write([]byte("ok")) }}
	for i := 0; i < midCount; i++ {
		handlers = append(handlers, func(ctx IContext) { ctx.Next() })
	}
	r.Url([]string{"GET"}, "/bench", handlers...)

	req := httptest.NewRequest("GET", "/bench", nil)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, transport.NewHttpRequest(req))
	}
}

func BenchmarkServe_0Mid(b *testing.B) { benchServe(b, 0) }
func BenchmarkServe_1Mid(b *testing.B) { benchServe(b, 1) }
func BenchmarkServe_3Mid(b *testing.B) { benchServe(b, 3) }
func BenchmarkServe_8Mid(b *testing.B) { benchServe(b, 8) }

// 长链用于把中间件派发的开销拉到噪声之上：短链下整个 ServeHTTP（路由匹配、
// 上下文池、19 次分配、约 2µs）完全淹没派发方式的差异。
func BenchmarkServe_50Mid(b *testing.B) { benchServe(b, 50) }
