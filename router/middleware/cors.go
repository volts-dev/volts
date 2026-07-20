package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/volts-dev/volts/router"
)

// CorsConfig 描述 CORS 策略。
//
// 安全约束：AllowOrigins 含 "*" 时不允许开启 AllowCredentials —— 这个组合被
// Fetch 规范禁止（浏览器会拒绝带凭证的通配响应），而"修好"它的惯常做法是把
// 请求的 Origin 原样回显，那等于允许任意站点携带用户 Cookie 读取本服务的响应，
// 是一个完整的跨站数据泄漏漏洞。NewCors 遇到该组合直接 panic，在启动期暴露，
// 而不是留到线上表现为"CORS 时好时坏"。
type CorsConfig struct {
	// AllowOrigins 允许的来源，需精确匹配（含 scheme 与端口），如
	// "https://app.example.com"。特例 "*" 表示任意来源，此时不得开启
	// AllowCredentials。为空视为 ["*"]。
	AllowOrigins []string

	// AllowMethods 预检放行的方法。为空使用 DefaultCorsMethods。
	AllowMethods []string

	// AllowHeaders 预检放行的请求头。为空时回显浏览器的
	// Access-Control-Request-Headers —— 即"按需放行"，比通配 "*" 精确，
	// 且与 AllowCredentials 兼容（通配头同样会被带凭证的请求拒绝）。
	AllowHeaders []string

	// ExposeHeaders 允许前端 JS 读取的响应头。跨域下默认只能读 6 个安全头。
	ExposeHeaders []string

	// AllowCredentials 是否允许携带 Cookie / Authorization。
	// 开启时 AllowOrigins 不得含 "*"。
	AllowCredentials bool

	// MaxAge 预检结果的缓存时长。<=0 则不下发 Access-Control-Max-Age。
	MaxAge time.Duration
}

// DefaultCorsMethods 是未指定 AllowMethods 时放行的方法集合。
// 不含 TRACE —— 它会回显请求内容，在跨域语境下没有正当用途。
var DefaultCorsMethods = []string{
	http.MethodGet,
	http.MethodHead,
	http.MethodPost,
	http.MethodPut,
	http.MethodPatch,
	http.MethodDelete,
	http.MethodOptions,
}

// TCors 是 CORS 中间件，实现 router.IMiddleware。
//
// 用法遵循本框架的中间件装配模型 —— RegisterMiddleware 只登记构造器，真正的
// 绑定靠控制器内嵌：
//
//	type MyCtrl struct {
//	    middleware.TCors    // 内嵌即启用；必须是第 1 个字段
//	}
//	func (c MyCtrl) Index(ctx *router.THttpContext) { ... }
//
//	r.RegisterMiddleware(middleware.NewCors)
//	r.Url([]string{"GET"}, "/", MyCtrl.Index)
//
// 方法为值接收器，因此值内嵌（而非指针内嵌）即可满足 IMiddleware ——
// handler.init 是拿控制器的**值**去做接口断言的，指针接收器会静默落空。
type TCors struct {
	handle func(router.IContext)
}

func (self TCors) Name() string { return "cors" }

// Transports 声明 CORS 只对 HTTP 有意义 —— 它读写的 Origin / Vary /
// Access-Control-* 全是 HTTP 头，RPC 侧没有对应语义。
// 声明后本中间件在 RPC handler 的链上装配期即被筛掉，请求路径零开销。
func (self TCors) Transports() []router.TransportType {
	return []router.TransportType{router.HttpHandler}
}

func (self TCors) Handler(ctx router.IContext) {
	if self.handle == nil {
		// 未经构造器初始化（例如控制器内嵌但忘了 RegisterMiddleware），
		// 放行而非 panic —— 缺 CORS 头顶多是跨域请求被浏览器拦，
		// 打挂整个服务则严重得多。
		ctx.Next()
		return
	}
	self.handle(ctx)
}

// NewCors 是可直接交给 RegisterMiddleware 的默认 CORS 中间件构造器：
//
//	r.RegisterMiddleware(middleware.NewCors)
//
// 策略为「任意来源可读、但不携带凭证」—— 浏览器不会带上 Cookie，因此不构成
// 越权读取。需要携带凭证请用 Cors(CorsConfig{...}) 明确列出来源。
func NewCors(router.IRouter) router.IMiddleware {
	return &TCors{handle: corsHandler(CorsConfig{})}
}

// Cors 按给定策略构造 CORS 中间件构造器：
//
//	r.RegisterMiddleware(middleware.Cors(middleware.CorsConfig{
//	    AllowOrigins:     []string{"https://app.example.com"},
//	    AllowCredentials: true,
//	}))
//
// AllowOrigins 含 "*" 且 AllowCredentials 为真时 panic（见 CorsConfig 说明）。
//
// NOTE 签名变更：此前是 Cors() func(router.IContext)，只能作路由级中间件用
// （Url(methods, path, 主控制器, middleware.Cors())）。改为返回
// func(IRouter) IMiddleware 之后两条路径都能走：路由级 mids 认这个类型
// （见 handler.addMiddlewares），RegisterMiddleware + 控制器内嵌也认。
func Cors(cfg CorsConfig) func(router.IRouter) router.IMiddleware {
	handle := corsHandler(cfg) // 配置校验在此刻完成，启动期即暴露问题
	return func(router.IRouter) router.IMiddleware {
		return &TCors{handle: handle}
	}
}

// corsHandler 把配置编译成实际的请求处理闭包。
// 所有字符串拼接与白名单建表都在此处一次完成，请求路径上只做查表。
func corsHandler(cfg CorsConfig) func(router.IContext) {
	if len(cfg.AllowOrigins) == 0 {
		cfg.AllowOrigins = []string{"*"}
	}
	if len(cfg.AllowMethods) == 0 {
		cfg.AllowMethods = DefaultCorsMethods
	}

	allowAny := false
	allowed := make(map[string]struct{}, len(cfg.AllowOrigins))
	for _, o := range cfg.AllowOrigins {
		if o == "*" {
			allowAny = true
			continue
		}
		allowed[normalizeOrigin(o)] = struct{}{}
	}

	if allowAny && cfg.AllowCredentials {
		panic("middleware: CORS 配置不安全 —— AllowOrigins 含 \"*\" 时不能开启 " +
			"AllowCredentials，否则任意站点都能携带用户凭证读取本服务响应。" +
			"请把 AllowOrigins 改为精确的来源列表。")
	}

	methods := strings.Join(cfg.AllowMethods, ", ")
	allowHeaders := strings.Join(cfg.AllowHeaders, ", ")
	exposeHeaders := strings.Join(cfg.ExposeHeaders, ", ")
	maxAge := ""
	if cfg.MaxAge > 0 {
		maxAge = strconv.Itoa(int(cfg.MaxAge.Seconds()))
	}

	return func(ctx router.IContext) {
		c, ok := ctx.(*router.THttpContext)
		if !ok {
			// 非 HTTP 上下文（RPC）无 CORS 语义，放行即可。
			// 此前这里直接 return，会把整条中间件链掐断。
			ctx.Next()
			return
		}

		origin := c.Request().Header().Get("Origin")
		if origin == "" {
			// 非跨域请求：不下发任何 CORS 头，避免污染同源响应的缓存。
			c.Next()
			return
		}

		// 响应随 Origin 变化，必须声明，否则中间缓存/CDN 会把 A 站的
		// 放行响应喂给 B 站。即便本次判定为不放行也要带上。
		c.Header().Add("Vary", "Origin")

		_, hit := allowed[normalizeOrigin(origin)]
		if !hit && !allowAny {
			// 来源不在白名单：不下发放行头，由浏览器拦截。
			// 预检直接 204 结束，简单请求继续走业务（响应会被浏览器丢弃）。
			if isPreflight(c) {
				c.Abort("", http.StatusNoContent)
				return
			}
			c.Next()
			return
		}

		if cfg.AllowCredentials {
			// 带凭证时必须回显精确来源，通配符会被浏览器拒绝。
			c.Header().Set("Access-Control-Allow-Origin", origin)
			c.Header().Set("Access-Control-Allow-Credentials", "true")
		} else if allowAny {
			c.Header().Set("Access-Control-Allow-Origin", "*")
		} else {
			c.Header().Set("Access-Control-Allow-Origin", origin)
		}

		if !isPreflight(c) {
			if exposeHeaders != "" {
				c.Header().Set("Access-Control-Expose-Headers", exposeHeaders)
			}
			c.Next()
			return
		}

		// —— 预检 ——
		c.Header().Add("Vary", "Access-Control-Request-Method")
		c.Header().Add("Vary", "Access-Control-Request-Headers")
		c.Header().Set("Access-Control-Allow-Methods", methods)

		if allowHeaders != "" {
			c.Header().Set("Access-Control-Allow-Headers", allowHeaders)
		} else if reqHeaders := c.Request().Header().Get("Access-Control-Request-Headers"); reqHeaders != "" {
			// 按需回显：等价于放行浏览器本次询问的头，但不会像 "*" 那样
			// 在带凭证场景下失效。
			c.Header().Set("Access-Control-Allow-Headers", reqHeaders)
		}

		if maxAge != "" {
			c.Header().Set("Access-Control-Max-Age", maxAge)
		}

		// 预检到此为止，绝不能再进业务 handler。
		// 此前的实现 Abort 之后仍然调用了 Next()。
		c.Abort("", http.StatusNoContent)
	}
}

// isPreflight 判定是否为预检请求。只看 OPTIONS 不够 —— 业务可能自己实现
// OPTIONS 端点，只有带 Access-Control-Request-Method 的才是浏览器预检。
func isPreflight(c *router.THttpContext) bool {
	return c.Request().Method == http.MethodOptions &&
		c.Request().Header().Get("Access-Control-Request-Method") != ""
}

// normalizeOrigin 统一大小写与尾斜杠后再比较。Origin 的 scheme/host 大小写
// 不敏感，但路径部分本不该出现 —— 配置里手滑写成 "https://a.com/" 很常见。
func normalizeOrigin(o string) string {
	return strings.ToLower(strings.TrimRight(o, "/"))
}
