package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/volts-dev/volts/router"
	"github.com/volts-dev/volts/transport"
)

// csrfCtrl 承载 CSRF 中间件的测试控制器（内嵌即启用）。
type csrfCtrl struct {
	TCSRF
}

func (c csrfCtrl) Business(ctx *router.THttpContext) {
	ctx.Write([]byte("business"))
}

func serveCSRF(t *testing.T, creator func(router.IRouter) router.IMiddleware,
	method, path string, headers map[string]string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()

	r := router.New()
	t.Cleanup(func() { r.Close() })

	r.RegisterMiddleware(creator)
	r.Url([]string{"GET", "POST"}, path, csrfCtrl.Business)

	req := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, transport.NewHttpRequest(req))
	return w
}

// 取出响应里下发的 CSRF cookie。
func csrfCookieFrom(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range (&http.Response{Header: w.Header()}).Cookies() {
		if c.Name == DefaultCSRFCookieName {
			return c
		}
	}
	return nil
}

// 首次访问必须下发 token 并放行。
//
// 顺序很关键：若先校验再补发，首访没有 Cookie 会被直接拒，前端永远拿不到
// token，陷入死锁。
func TestCSRFIssuesTokenOnFirstVisit(t *testing.T) {
	w := serveCSRF(t, NewCSRF, "GET", "/c1", nil, nil)

	c := csrfCookieFrom(w)
	if c == nil {
		t.Fatal("首次访问未下发 CSRF cookie")
	}
	if c.Value == "" {
		t.Fatal("下发的 token 为空")
	}
	if !strings.Contains(w.Body.String(), "business") {
		t.Fatal("首次访问应当放行")
	}
}

// 下发的 Cookie 必须带 SameSite —— 它是双提交方案的配套前提，
// 缺了它攻击者可用 Cookie 覆写等手法绕过。
func TestCSRFCookieHasSameSite(t *testing.T) {
	w := serveCSRF(t, NewCSRF, "GET", "/c2", nil, nil)

	c := csrfCookieFrom(w)
	if c == nil {
		t.Fatal("未下发 cookie")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", c.SameSite)
	}
}

// 双提交方案要求前端 JS 能读到 token 才能回填请求头，故不能是 HttpOnly。
func TestCSRFCookieIsReadableByScript(t *testing.T) {
	w := serveCSRF(t, NewCSRF, "GET", "/c3", nil, nil)

	c := csrfCookieFrom(w)
	if c == nil {
		t.Fatal("未下发 cookie")
	}
	if c.HttpOnly {
		t.Error("CSRF token cookie 不能是 HttpOnly —— 前端需要读它回填请求头")
	}
}

// 不带 token 的写请求必须被拒。这是整个中间件存在的理由。
func TestCSRFRejectsUnsafeMethodWithoutToken(t *testing.T) {
	w := serveCSRF(t, NewCSRF, "POST", "/c4", nil, nil)

	if w.Code != http.StatusForbidden {
		t.Errorf("状态码 = %d, want 403", w.Code)
	}
	if strings.Contains(w.Body.String(), "business") {
		t.Error("无 token 的 POST 穿透到了业务 handler")
	}
}

// token 匹配时放行。
func TestCSRFAcceptsMatchingToken(t *testing.T) {
	token := "test-token-value"

	w := serveCSRF(t, NewCSRF, "POST", "/c5",
		map[string]string{DefaultCSRFHeaderName: token},
		[]*http.Cookie{{Name: DefaultCSRFCookieName, Value: token}},
	)

	if !strings.Contains(w.Body.String(), "business") {
		t.Fatalf("token 匹配却被拒: code=%d body=%q", w.Code, w.Body.String())
	}
}

// 请求头带的 token 与 Cookie 不一致时必须拒绝 ——
// 攻击者能让浏览器携带 Cookie，但读不到它的值，因此填不出正确的头。
func TestCSRFRejectsMismatchedToken(t *testing.T) {
	w := serveCSRF(t, NewCSRF, "POST", "/c6",
		map[string]string{DefaultCSRFHeaderName: "attacker-guess"},
		[]*http.Cookie{{Name: DefaultCSRFCookieName, Value: "real-token"}},
	)

	if w.Code != http.StatusForbidden {
		t.Errorf("状态码 = %d, want 403", w.Code)
	}
	if strings.Contains(w.Body.String(), "business") {
		t.Error("token 不匹配却穿透到了业务 handler")
	}
}

// 安全方法（GET/HEAD/OPTIONS/TRACE）不做校验 —— 按 RFC 它们应当无副作用。
func TestCSRFSkipsSafeMethods(t *testing.T) {
	w := serveCSRF(t, NewCSRF, "GET", "/c7", nil,
		[]*http.Cookie{{Name: DefaultCSRFCookieName, Value: "whatever"}})

	if !strings.Contains(w.Body.String(), "business") {
		t.Fatalf("GET 被拦下了: code=%d", w.Code)
	}
}

// Skip 回调可放行确实无法携带 token 的端点（如第三方 webhook）。
func TestCSRFSkipCallback(t *testing.T) {
	mw := CSRF(CSRFConfig{
		Skip: func(ctx *router.THttpContext) bool {
			return strings.HasPrefix(ctx.Request().URL.Path, "/c8")
		},
	})

	w := serveCSRF(t, mw, "POST", "/c8", nil, nil)

	if !strings.Contains(w.Body.String(), "business") {
		t.Fatalf("Skip 未生效: code=%d body=%q", w.Code, w.Body.String())
	}
}

// 每次签发的 token 必须不同 —— 固定 token 等于没有防护。
func TestCSRFTokensAreUnique(t *testing.T) {
	seen := map[string]bool{}

	for i := 0; i < 20; i++ {
		w := serveCSRF(t, NewCSRF, "GET", "/c9", nil, nil)
		c := csrfCookieFrom(w)
		if c == nil {
			t.Fatal("未下发 cookie")
		}
		if seen[c.Value] {
			t.Fatalf("token 重复出现: %q —— 随机源可能有问题", c.Value)
		}
		seen[c.Value] = true
	}
}

// Secure 配置需要如实反映到 Cookie 上。
func TestCSRFSecureOption(t *testing.T) {
	mw := CSRF(CSRFConfig{Secure: true})
	w := serveCSRF(t, mw, "GET", "/c10", nil, nil)

	c := csrfCookieFrom(w)
	if c == nil {
		t.Fatal("未下发 cookie")
	}
	if !c.Secure {
		t.Error("Secure=true 未反映到 Cookie")
	}
}
