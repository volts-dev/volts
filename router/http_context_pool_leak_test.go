package router

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/volts-dev/volts/transport"
)

// ★★★ 池化的 context 不许串给下一个请求。
//
// THttpContext 按**路由**池化复用。中间件/处理器往 `ctx.Context()` 里塞的值
// （vectors 里是 `"AuthSession"` 会话、调用方身份、租户作用域）如果活过 reset，
// 下一个复用到同一路由的请求就会读到**上一位用户**的身份。
//
// 最短的真实路径：鉴权中间件在认之前就拒掉（Token 为空 → 401）时不会挂 AuthSession，
// 于是写响应那一路读到的是上一个请求留下的那份。2026-09-24 真栈量到：一个完全不带
// Auth 头的请求拿到了只该给管理员的错误诊断附件。
//
// TRpcContext.reset 早就清了并在注释里写明了这条危害，HTTP 这边是漏的。
func TestHttpContext_ResetClearsContextValues(t *testing.T) {
	ctx := &THttpContext{}

	// 第一个请求：中间件往 context 里塞了身份。
	ctx.Context(context.WithValue(context.Background(), "AuthSession", "alice"))
	if got := ctx.Context().Value("AuthSession"); got != "alice" {
		t.Fatalf("同一个请求内应读得到自己塞的值，得到 %v", got)
	}

	// 回池、被下一个请求取出。
	ctx.reset(nil, transport.NewHttpRequest(httptest.NewRequest("POST", "/api/session/set_debug", nil)))

	if got := ctx.Context().Value("AuthSession"); got != nil {
		t.Fatalf("★ 上一个请求的身份串到了下一个请求：AuthSession = %v", got)
	}
	// 清成 nil 之后 Context() 仍要给一个可用的 context，不能返回 nil。
	if ctx.Context() == nil {
		t.Fatal("Context() 返回了 nil —— 调用方一律直接用它，不判空")
	}
}
