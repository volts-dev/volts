package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/volts-dev/volts/router"
	"github.com/volts-dev/volts/transport"
)

// corsCtrl 是承载 CORS 中间件的测试控制器。
// 内嵌 TCors 即启用 —— 这是本框架唯一的中间件绑定方式（见 handler.init）。
type corsCtrl struct {
	TCors
}

func (c corsCtrl) Business(ctx *router.THttpContext) {
	ctx.Write([]byte("business"))
}

// serve 在一条挂了 CORS 中间件的路由上跑一个请求，返回响应记录器。
// 走的是框架真实的装配路径：RegisterMiddleware 登记构造器 + 控制器内嵌绑定。
func serve(t *testing.T, creator func(router.IRouter) router.IMiddleware, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	r := router.New()
	t.Cleanup(func() { r.Close() })

	r.RegisterMiddleware(creator)
	r.Url([]string{"GET", "POST", "OPTIONS"}, path, corsCtrl.Business)

	req := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, transport.NewHttpRequest(req))
	return w
}

// 通配来源 + 携带凭证是被 Fetch 规范禁止的组合，也是一个完整的跨站数据泄漏
// 隐患。构造期就必须炸掉，而不是留到线上表现为"CORS 时好时坏"。
func TestNewCors_PanicsOnWildcardWithCredentials(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("AllowOrigins=[\"*\"] + AllowCredentials=true 必须 panic")
		}
	}()
	Cors(CorsConfig{AllowOrigins: []string{"*"}, AllowCredentials: true})
}

// 默认 NewCors 必须是"任意来源可读、但不带凭证"。
// 旧实现同时下发 * 与 Allow-Credentials:true —— 一旦有人把 * 改成回显 Origin
// "修好"它，就变成任意站点可携带用户 Cookie 读取本服务响应。
func TestCors_DefaultDoesNotAllowCredentials(t *testing.T) {
	w := serve(t, NewCors, "GET", "/c1", map[string]string{"Origin": "https://evil.example"})

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Allow-Origin = %q, want %q", got, "*")
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("默认配置不得下发 Allow-Credentials, 得到 %q", got)
	}
}

// 白名单外的来源不得拿到放行头。
func TestCors_RejectsUnlistedOrigin(t *testing.T) {
	mw := Cors(CorsConfig{
		AllowOrigins:     []string{"https://app.example.com"},
		AllowCredentials: true,
	})
	w := serve(t, mw, "GET", "/c2", map[string]string{"Origin": "https://evil.example"})

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("未列入白名单的来源拿到了放行头: %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("未列入白名单的来源拿到了 Allow-Credentials: %q", got)
	}
}

// 白名单内的来源：带凭证时必须回显精确 Origin（通配符会被浏览器拒绝）。
func TestCors_EchoesListedOriginWithCredentials(t *testing.T) {
	mw := Cors(CorsConfig{
		AllowOrigins:     []string{"https://app.example.com"},
		AllowCredentials: true,
	})
	w := serve(t, mw, "GET", "/c3", map[string]string{"Origin": "https://app.example.com"})

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("Allow-Origin = %q, want 精确回显", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("Allow-Credentials = %q, want true", got)
	}
}

// Vary: Origin 必须下发，否则中间缓存/CDN 会把 A 站的放行响应喂给 B 站。
func TestCors_SetsVaryOrigin(t *testing.T) {
	w := serve(t, NewCors, "GET", "/c4", map[string]string{"Origin": "https://a.example"})

	if !strings.Contains(w.Header().Get("Vary"), "Origin") {
		t.Errorf("Vary = %q, 必须含 Origin", w.Header().Get("Vary"))
	}
}

// 同源请求（无 Origin 头）不应被塞 CORS 头，避免污染同源响应的缓存条目。
func TestCors_SkipsNonCorsRequest(t *testing.T) {
	w := serve(t, NewCors, "GET", "/c5", nil)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("同源请求被下发了 Allow-Origin: %q", got)
	}
	if body := w.Body.String(); !strings.Contains(body, "business") {
		t.Errorf("同源请求未进入业务 handler, body = %q", body)
	}
}

// 预检必须以 204 结束，绝不能穿透到业务 handler。
// 旧实现 Abort() 之后仍然调用了 Next()，业务代码会被 OPTIONS 请求触发一遍。
func TestCors_PreflightDoesNotReachHandler(t *testing.T) {
	w := serve(t, NewCors, "OPTIONS", "/c6", map[string]string{
		"Origin":                        "https://a.example",
		"Access-Control-Request-Method": "POST",
	})

	if w.Code != http.StatusNoContent {
		t.Errorf("预检状态码 = %d, want 204", w.Code)
	}
	if body := w.Body.String(); strings.Contains(body, "business") {
		t.Errorf("预检穿透到了业务 handler, body = %q", body)
	}
	if got := w.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Error("预检未下发 Allow-Methods")
	}
}

// 未配置 AllowHeaders 时按需回显浏览器询问的头 —— 比通配 "*" 精确，
// 且在带凭证场景下依然有效（"*" 会被拒）。
func TestCors_PreflightEchoesRequestedHeaders(t *testing.T) {
	mw := Cors(CorsConfig{AllowOrigins: []string{"https://app.example.com"}})
	w := serve(t, mw, "OPTIONS", "/c7", map[string]string{
		"Origin":                         "https://app.example.com",
		"Access-Control-Request-Method":  "POST",
		"Access-Control-Request-Headers": "X-Auth, Content-Type",
	})

	if got := w.Header().Get("Access-Control-Allow-Headers"); got != "X-Auth, Content-Type" {
		t.Errorf("Allow-Headers = %q, want 回显请求头", got)
	}
}

// 业务自己实现的 OPTIONS 端点（不带 Access-Control-Request-Method）
// 不是预检，必须照常进入 handler。
func TestCors_PlainOptionsIsNotPreflight(t *testing.T) {
	w := serve(t, NewCors, "OPTIONS", "/c8", map[string]string{"Origin": "https://a.example"})

	if body := w.Body.String(); !strings.Contains(body, "business") {
		t.Errorf("非预检的 OPTIONS 被拦下了, code = %d body = %q", w.Code, body)
	}
}
