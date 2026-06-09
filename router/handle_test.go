package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/volts-dev/volts/transport"
)

type hReq struct {
	Name string `json:"name"`
}
type hRsp struct {
	Msg string `json:"msg"`
}

// TestApi_ConcreteHTTPContext 验证 Api 把 context 参数化为具体 *THttpContext：
// handler 直接拿到具体类型，运行期由 ctx.(C) 还原。
func TestApi_ConcreteHTTPContext(t *testing.T) {
	r := New()
	defer close(r.exit)

	grp := NewGroup()
	grp.Api[*THttpContext, hReq, hRsp]("POST", "/api-hi", func(c *THttpContext, in *hReq) (*hRsp, error) {
		// c 是具体的 *THttpContext，可用其完整方法
		_ = c.Response()
		return &hRsp{Msg: "hi " + in.Name}, nil
	}, OpSummary("api hello"))
	r.RegisterGroup(grp)

	req := httptest.NewRequest("POST", "/api-hi", strings.NewReader(`{"name":"bob"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, transport.NewHttpRequest(req))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "hi bob") {
		t.Fatalf("api round-trip bad: %d %s", w.Code, w.Body.String())
	}
}

func TestHandle_RegistersRouteWithMeta(t *testing.T) {
	r := New()
	defer close(r.exit)
	grp := NewGroup()
	route := Handle(grp, "POST", "/hello", func(ctx IContext, in *hReq) (*hRsp, error) {
		return &hRsp{Msg: "hi " + in.Name}, nil
	}, OpSummary("hello"))

	if route.meta == nil {
		t.Fatal("route.meta not set by Handle")
	}
	op := route.meta.(*Operation)
	if op.Request == nil || op.Request.Type != "hReq" {
		t.Fatalf("request schema missing: %+v", op.Request)
	}
	if op.Response == nil || op.Response.Type != "hRsp" {
		t.Fatalf("response schema missing: %+v", op.Response)
	}
}

func TestHandle_HTTPRoundTrip(t *testing.T) {
	r := New()
	defer close(r.exit)
	grp := NewGroup()
	Handle(grp, "POST", "/hello", func(ctx IContext, in *hReq) (*hRsp, error) {
		return &hRsp{Msg: "hi " + in.Name}, nil
	})
	r.RegisterGroup(grp)

	httpReq := httptest.NewRequest("POST", "/hello", strings.NewReader(`{"name":"bob"}`))
	httpReq.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, transport.NewHttpRequest(httpReq))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "hi bob") {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}

type hQueryReq struct {
	Name string `json:"name" in:"query"`
	Note string `json:"note"`
}

func TestHandle_QueryFieldDoesNotClobberBody(t *testing.T) {
	r := New()
	defer close(r.exit)
	grp := NewGroup()
	Handle(grp, "POST", "/q", func(ctx IContext, in *hQueryReq) (*hRsp, error) {
		return &hRsp{Msg: in.Name + "|" + in.Note}, nil
	})
	r.RegisterGroup(grp)

	httpReq := httptest.NewRequest("POST", "/q", strings.NewReader(`{"name":"bob","note":"hi"}`))
	httpReq.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, transport.NewHttpRequest(httpReq))

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	// Name (in:"query", absent in query) must NOT be clobbered to "" — body value "bob" must survive.
	if !strings.Contains(w.Body.String(), "bob|hi") {
		t.Fatalf("body field clobbered: got %s", w.Body.String())
	}
}
