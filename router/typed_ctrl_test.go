package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/volts-dev/volts/transport"
)

// demoMiddleware 是测试用中间件，用于验证它被注入到 typed 控制器的字段里。
type demoMiddleware struct{}

func (m *demoMiddleware) Handler(ctx IContext) {} // no-op

// demoMidCtrl 声明一个中间件字段（Mid）+ Init/Ready，作为 typed Api 的控制器。
// 验证 typed handler 与反射控制器一致地享受中间件字段注入与生命周期回调。
type demoMidCtrl struct {
	Mid     *demoMiddleware // 期望被 handler.init 注入
	Inited  bool            // Init 是否被调用
	Readied bool            // Ready 是否被调用
}

func (c *demoMidCtrl) Init(cfg *ControllerConfig)  { c.Inited = true }
func (c *demoMidCtrl) Ready(cfg *ControllerConfig) { c.Readied = true }

type midProbeRsp struct {
	MidInjected bool `json:"mid_injected"`
	Inited      bool `json:"inited"`
	Readied     bool `json:"readied"`
}

// Probe 用值接收器，Ctrl 推断为 demoMidCtrl（值）。core 调用期会从 init 构建的
// *demoMidCtrl 解引用出带注入字段的副本。
func (c demoMidCtrl) Probe(ctx *THttpContext, in *hReq) (*midProbeRsp, error) {
	return &midProbeRsp{
		MidInjected: c.Mid != nil,
		Inited:      c.Inited,
		Readied:     c.Readied,
	}, nil
}

// TestApi_TypedHandlerMiddlewareInjection 验证经 Api 注册的 typed handler，其控制器
// 会被 handler.init 构建、注入中间件字段并调用 Init/Ready —— 与反射控制器行为一致。
func TestApi_TypedHandlerMiddlewareInjection(t *testing.T) {
	r := New()
	defer close(r.exit)

	// 注册中间件：名称取类型字符串 "router.demoMiddleware"，与控制器字段类型一致。
	r.RegisterMiddleware(func(IRouter) IMiddleware { return &demoMiddleware{} })

	grp := NewGroup()
	grp.Api[demoMidCtrl, *THttpContext, hReq, midProbeRsp]([]string{"POST"}, "/probe", demoMidCtrl.Probe)
	r.RegisterGroup(grp)

	req := httptest.NewRequest("POST", "/probe", strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, transport.NewHttpRequest(req))

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"mid_injected":true`) {
		t.Fatalf("middleware not injected into typed controller: %s", body)
	}
	if !strings.Contains(body, `"inited":true`) {
		t.Fatalf("Init not called on typed controller: %s", body)
	}
	if !strings.Contains(body, `"readied":true`) {
		t.Fatalf("Ready not called on typed controller: %s", body)
	}
}
