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
