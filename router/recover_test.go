package router

import (
	"net/http/httptest"
	"strings"
	"testing"

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
