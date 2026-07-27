package router

import (
	"context"
	"testing"

	"github.com/volts-dev/volts/internal/metadata"
)

type ctxProbeKey struct{}

// TRpcContext 走 rpcCtxPool 复用，reset 必须清空 self.context。
//
// ServeRPC 现在把 wire 帧头（含**调用方身份**）灌进这个 context，所以不清空不再只是
// "看到上一个请求的调试值"，而是**顶着上一个请求的身份执行**——池里取到的对象带着
// 别人的 Caller-Identity / 租户上下文，且完全无声。
//
// 这条与 THttpContext 那边清 hijacked 标志是同一类要求：池化对象的每个字段都必须在
// reset 里有明确归宿。
func TestRpcContextResetClearsContext(t *testing.T) {
	r := New()
	defer r.Close()

	ctx := NewRpcHandler(r)

	// 模拟上一个请求：身份挂在 context 上。
	prev := metadata.MergeContext(context.Background(), map[string]string{
		"Caller-Identity": "tenant-A",
	}, true)
	ctx.Context(context.WithValue(prev, ctxProbeKey{}, "leaked"))

	if got, _ := metadata.Get(ctx.Context(), "Caller-Identity"); got != "tenant-A" {
		t.Fatalf("前置条件不成立：身份没挂上去，got %q", got)
	}

	// 池回收后被下一个请求取出。
	ctx.reset(nil, nil, r, nil)

	if v := ctx.Context().Value(ctxProbeKey{}); v != nil {
		t.Errorf("reset 后仍能取到上一个请求塞的值 %v —— 池化对象串号", v)
	}
	if got, ok := metadata.Get(ctx.Context(), "Caller-Identity"); ok {
		t.Errorf("reset 后仍带着上一个请求的调用方身份 %q —— 这会导致顶着别人身份执行", got)
	}
}

// reset 之后 Context() 不能返回 nil（调用方无条件使用它）。
func TestRpcContextAfterResetReturnsUsableContext(t *testing.T) {
	r := New()
	defer r.Close()

	ctx := NewRpcHandler(r)
	ctx.Context(context.WithValue(context.Background(), ctxProbeKey{}, "x"))
	ctx.reset(nil, nil, r, nil)

	if c := ctx.Context(); c == nil {
		t.Fatal("reset 后 Context() 返回 nil")
	}
}
