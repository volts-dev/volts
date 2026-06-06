package volts

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/volts-dev/volts/router"
	"github.com/volts-dev/volts/server"
	"github.com/volts-dev/volts/transport"
)

type oaReq struct {
	Name string `json:"name"`
}
type oaRsp struct {
	Msg string `json:"msg"`
}

func TestOpenAPI_ServesSpecAndDocs(t *testing.T) {
	srv := server.New()
	r := srv.Config().Router.(*router.TRouter)

	grp := router.NewGroup()
	router.Handle(grp, "POST", "/hello", func(ctx router.IContext, in *oaReq) (*oaRsp, error) {
		return &oaRsp{Msg: "hi " + in.Name}, nil
	}, router.OpSummary("hello"))
	r.RegisterGroup(grp)

	app := New(
		Server(srv),
		OpenAPI(OpenAPITitle("T"), OpenAPIVersion("1.0.0")),
	)
	mountOpenAPI(app.Config())

	// /openapi.json
	req := httptest.NewRequest("GET", "/openapi.json", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, transport.NewHttpRequest(req))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "/hello") {
		t.Fatalf("openapi.json bad: %d %s", w.Code, w.Body.String())
	}

	// /docs
	req2 := httptest.NewRequest("GET", "/docs", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, transport.NewHttpRequest(req2))
	if w2.Code != http.StatusOK || !strings.Contains(w2.Body.String(), "openapi.json") {
		t.Fatalf("docs bad: %d %s", w2.Code, w2.Body.String())
	}
}
