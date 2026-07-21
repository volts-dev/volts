package middleware

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/volts-dev/volts/router"
)

// CSRF 防护采用「双提交 Cookie」（double-submit cookie）方案：
//
//	服务端下发一个随机 token 到 Cookie；前端把同一个 token 放进请求头
//	（或表单字段）再发回来；服务端比对两者是否一致。
//
// 攻击者可以让受害者的浏览器**携带** Cookie 发起跨站请求，但由于同源策略，
// 他读不到 Cookie 的内容，也就无法把正确的值填进请求头 —— 比对因此失败。
//
// 前提是 Cookie 必须带 SameSite（本中间件默认 Lax），否则攻击者虽然读不到值，
// 却可以用「Cookie 覆写」等手法绕过双提交。两者是配套的，不是二选一。
//
// 不做什么：
//   - 不对安全方法（GET/HEAD/OPTIONS/TRACE）做校验 —— 按 RFC 它们应当无副作用；
//     如果你的 GET 会改数据，那要修的是那个接口，不是在这里加校验
//   - 不替代鉴权。CSRF 防的是"借用受害者的身份"，不是"没有身份"

const (
	// DefaultCSRFCookieName 存放 token 的 Cookie 名。
	DefaultCSRFCookieName = "_csrf"
	// DefaultCSRFHeaderName 前端回传 token 的请求头。
	DefaultCSRFHeaderName = "X-CSRF-Token"
	// csrfTokenBytes token 的随机字节数。32 字节 = 256 bit，远超暴力破解可行范围。
	csrfTokenBytes = 32
)

// CSRFConfig 配置 CSRF 防护。
type CSRFConfig struct {
	// CookieName 存放 token 的 Cookie 名，空则用 DefaultCSRFCookieName。
	CookieName string
	// HeaderName 前端回传 token 的请求头，空则用 DefaultCSRFHeaderName。
	HeaderName string
	// FormField 允许从表单字段读取 token（传统表单提交场景）。空则不启用。
	FormField string
	// CookiePath Cookie 的 Path，空则为 "/"。
	CookiePath string
	// CookieDomain Cookie 的 Domain，空则不设置。
	CookieDomain string
	// Secure 是否只在 HTTPS 下发送该 Cookie。生产环境应当为 true。
	Secure bool
	// SameSite Cookie 的 SameSite 属性，零值按 Lax 处理。
	SameSite http.SameSite
	// Skip 返回 true 时跳过本次校验，用于放行 webhook 等确实无法携带 token 的端点。
	Skip func(ctx *router.THttpContext) bool
}

// TCSRF 是 CSRF 防护中间件，实现 router.IMiddleware。
//
// 方法为值接收器 —— handler.init 用控制器的**值**做接口断言，
// 指针接收器会静默落空（见 TCors 的同款说明）。
type TCSRF struct {
	handle func(router.IContext)
}

func (self TCSRF) Name() string { return "csrf" }

func (self TCSRF) Handler(ctx router.IContext) {
	if self.handle == nil {
		ctx.Next()
		return
	}
	self.handle(ctx)
}

// Transports 声明本中间件只适用于 HTTP —— Cookie 与请求头是 HTTP 概念，
// RPC 侧没有对应语义。声明后在 RPC handler 的链上装配期即被筛掉。
func (self TCSRF) Transports() []router.TransportType {
	return []router.TransportType{router.HttpHandler}
}

// NewCSRF 是可直接交给 Use / RegisterMiddleware 的默认 CSRF 中间件构造器。
//
// NOTE 默认 Secure=false，以免在本地 http 开发环境下 Cookie 直接发不出去。
// **生产环境请用 CSRF(CSRFConfig{Secure: true})**。
func NewCSRF(router.IRouter) router.IMiddleware {
	return &TCSRF{handle: csrfHandler(CSRFConfig{})}
}

// CSRF 按给定配置构造 CSRF 中间件构造器：
//
//	r.Use(middleware.CSRF(middleware.CSRFConfig{Secure: true}))
func CSRF(cfg CSRFConfig) func(router.IRouter) router.IMiddleware {
	handle := csrfHandler(cfg)
	return func(router.IRouter) router.IMiddleware {
		return &TCSRF{handle: handle}
	}
}

func csrfHandler(cfg CSRFConfig) func(router.IContext) {
	if cfg.CookieName == "" {
		cfg.CookieName = DefaultCSRFCookieName
	}
	if cfg.HeaderName == "" {
		cfg.HeaderName = DefaultCSRFHeaderName
	}
	if cfg.CookiePath == "" {
		cfg.CookiePath = "/"
	}
	// 同 THttpContext.SetCookie 的说明：SameSiteDefaultMode 是 iota+1（值为 1），
	// 而未赋值的零值是 0，只比 DefaultMode 会漏掉最常见的"没填"情形。
	if cfg.SameSite <= http.SameSiteDefaultMode {
		cfg.SameSite = http.SameSiteLaxMode
	}

	return func(ctx router.IContext) {
		c, ok := ctx.(*router.THttpContext)
		if !ok {
			ctx.Next()
			return
		}

		if cfg.Skip != nil && cfg.Skip(c) {
			c.Next()
			return
		}

		cookieToken := readCSRFCookie(c, cfg.CookieName)

		// 没有 token 就补发一个。注意**先补发再校验**：首次访问必然没有
		// Cookie，若直接拒绝，前端永远拿不到 token，陷入死锁。
		if cookieToken == "" {
			token, err := newCSRFToken()
			if err != nil {
				c.Abort("csrf: 生成 token 失败", http.StatusInternalServerError)
				return
			}
			cookieToken = token
			c.SetCookie(&http.Cookie{
				Name:  cfg.CookieName,
				Value: token,
				Path:  cfg.CookiePath,
				// 不能设 HttpOnly：双提交方案要求前端 JS 能读到它才能回填请求头。
				// 这不构成泄漏面 —— token 本身不是凭证，跨站方读不到它正是防护点。
				Domain:   cfg.CookieDomain,
				Secure:   cfg.Secure,
				SameSite: cfg.SameSite,
			})
		}

		// 安全方法按 RFC 应当无副作用，不做校验
		if isSafeMethod(c.Request().Method) {
			c.Next()
			return
		}

		sent := c.Request().Header().Get(cfg.HeaderName)
		if sent == "" && cfg.FormField != "" {
			sent = c.Request().Request.FormValue(cfg.FormField)
		}

		// 定长比较，避免通过响应时间侧信道逐字节猜 token
		if sent == "" || subtle.ConstantTimeCompare([]byte(sent), []byte(cookieToken)) != 1 {
			c.Abort("csrf: token 校验失败", http.StatusForbidden)
			return
		}

		c.Next()
	}
}

func readCSRFCookie(c *router.THttpContext, name string) string {
	cookie, err := c.Request().Request.Cookie(name)
	if err != nil || cookie == nil {
		return ""
	}
	return cookie.Value
}

func newCSRFToken() (string, error) {
	buf := make([]byte, csrfTokenBytes)
	// crypto/rand：math/rand 的序列可预测，攻击者能直接算出 token
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// isSafeMethod 判定是否为 RFC 定义的安全方法（不应产生副作用）。
func isSafeMethod(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	}
	return false
}
