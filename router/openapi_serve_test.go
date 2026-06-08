package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/volts-dev/volts/transport"
)

// TestOpenAPIGroup_ServesSpecAndDocs 验证：开启 UseOpenAPI 后，OpenAPIGroup
// 从已注册路由构建冻结 spec，并通过 /openapi.json 与 /docs 提供（类比 pprof 开关）。
func TestOpenAPIGroup_ServesSpecAndDocs(t *testing.T) {
	r := New()
	defer close(r.exit)

	grp := NewGroup()
	Handle(grp, "POST", "/hello-oag", func(ctx IContext, in *hReq) (*hRsp, error) {
		return &hRsp{Msg: "hi " + in.Name}, nil
	}, OpSummary("hello"))
	r.RegisterGroup(grp)

	// 模拟 server 在 cfg.UseOpenAPI 为真时的注册动作
	r.RegisterGroup(OpenAPIGroup(r))

	// /openapi.json 应包含已注册的业务路由
	req := httptest.NewRequest("GET", DefaultOpenAPISpecPath, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, transport.NewHttpRequest(req))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "/hello-oag") {
		t.Fatalf("openapi.json bad: %d %s", w.Code, w.Body.String())
	}

	// /docs 应是引用 spec 的 Scalar 页面
	req2 := httptest.NewRequest("GET", DefaultOpenAPIDocsPath, nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, transport.NewHttpRequest(req2))
	if w2.Code != http.StatusOK || !strings.Contains(w2.Body.String(), DefaultOpenAPISpecPath) {
		t.Fatalf("docs bad: %d %s", w2.Code, w2.Body.String())
	}
}
