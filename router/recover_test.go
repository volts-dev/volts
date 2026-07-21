package router

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/volts-dev/cacher/memory"
	"github.com/volts-dev/volts/transport"
)

// TestRecoverHandler_WritesErrorResponse 验证 handler panic 被 recover 后，客户端
// 能收到错误响应而非空回复——此前默认恢复处理只记日志不回写，HTTP 客户端拿到空 body、
// RPC 客户端在连接关闭时只看到 EOF，无从得知失败原因。
func TestRecoverHandler_WritesErrorResponse(t *testing.T) {
	r := New()
	defer close(r.exit)

	grp := NewGroup()
	grp.Url([]string{"POST"}, "/panic-me", func(c *THttpContext) {
		panic("boom for test")
	})
	r.RegisterGroup(grp)

	req := httptest.NewRequest("POST", "/panic-me", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, transport.NewHttpRequest(req))

	if w.Body.Len() == 0 {
		t.Fatalf("panic recovery wrote no response body; client would see an empty reply/EOF")
	}
	if !strings.Contains(w.Body.String(), "panic") {
		t.Fatalf("expected panic error message in body, got: %q", w.Body.String())
	}
}

// handler panic 后必须归还对象池。
//
// 此前 reset + Put 写在 handler.Invoke 之后，panic 会把这两步整个跳过：该
// handler 对象永远回不了池，下一个请求只能重新 clone 模型并走一遍 init
// （反射建控制器、注入中间件）。恰恰是"业务正在 panic"这种最该省资源的时刻，
// 代价被放大 —— panic 风暴下每个请求都在做全量重建。
func TestPanicReturnsHandlerToPool(t *testing.T) {
	r := New()
	defer r.Close()

	grp := NewGroup()
	rt := grp.Url([]string{"POST"}, "/panic-pool", func(c *THttpContext) {
		panic("boom for pool test")
	})
	r.RegisterGroup(grp)

	ids := rt.Handlers()
	if len(ids) == 0 {
		t.Fatal("路由没有绑定 handler")
	}
	id := ids[0]

	// 打一个必然 panic 的请求（由 router 的 Recover 兜住）
	req := httptest.NewRequest("POST", "/panic-pool", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(httptest.NewRecorder(), transport.NewHttpRequest(req))

	// 池中应当能取回一个 handler。修复前这里恒为空 —— 从来没有 Put 过。
	v, ok := defaultHandlerManager.handlerPool.Load(id)
	if !ok {
		t.Fatal("panic 后 handler 未归还：对象池里根本没有该 id 的条目")
	}

	cache, ok := v.(memory.StackCache)
	if !ok {
		t.Fatalf("对象池条目类型异常: %T", v)
	}
	if cache.Pop() == nil {
		t.Fatal("panic 后 handler 未归还：对象池为空")
	}
}
