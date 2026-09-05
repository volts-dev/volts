// Package router handles message serving and routing logic for both HTTP and RPC protocols.
package router

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/volts-dev/template"
	"github.com/volts-dev/utils"
	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/internal/addr"
	igoroutine "github.com/volts-dev/volts/internal/goroutine"
	"github.com/volts-dev/volts/internal/metadata"
	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/transport"
)

var log = logger.New("router")

type (
	// IRouter defines the interface for serving messages and managing endpoints.
	IRouter interface {
		// Config retrieves the current router configuration.
		Config() *Config
		// String returns the name/description of the router.
		String() string
		// Handler returns the underlying handler implementation (e.g., ServeHTTP).
		Handler() interface{}
		// Register adds an endpoint to the router's tree.
		Register(ep *registry.Endpoint) error
		// Deregister removes an endpoint from the router's tree.
		Deregister(ep *registry.Endpoint) error
		// Endpoints lists all endpoints registered in the router.
		Endpoints() (services map[*TGroup][]*registry.Endpoint)
		// RegisterMiddleware adds global middlewares to the router.
		RegisterMiddleware(middlewares ...func(IRouter) IMiddleware)
		// Use 把中间件挂到全局链，对所有路由生效。
		Use(middlewares ...any)
		// Middlewares 返回全局链的快照。
		Middlewares() []any
		// RegisterGroup combines one or more groups into the router.
		RegisterGroup(groups ...IGroup)
		// PrintRoutes outputs the current routing tree to the logger.
		PrintRoutes()
	}

	// TRouter is the default implementation of the IRouter interface.
	TRouter struct {
		sync.RWMutex
		// TGroup provides base group functionality for path management.
		*TGroup
		// config holds the router's runtime options.
		config *Config
		// middleware manages the router's middleware stack.
		middleware *TMiddlewareManager
		// template provides HTML template rendering capabilities.
		template *template.TTemplateSet
		// respPool caches THttpResponse objects for better performance.
		respPool sync.Pool
		// httpCtxPool stores sync.Pools for THttpContext per route ID.
		httpCtxPool sync.Map
		// rpcCtxPool stores sync.Pools for TRpcContext per route ID.
		rpcCtxPool sync.Map
		// globalMiddlewares 是 Use() 挂上的全局链，在 handler.init 时前置到每条
		// 路由的 funcs 最前面。存原始声明而非编译结果 —— 编译要按 handler 的
		// TransportType 筛选，只能等到 init 时才知道。
		globalMiddlewares []any
		// exit channel signals background tasks to stop.
		exit chan bool
		// closeOnce guarantees the exit channel is closed exactly once (Close is idempotent).
		closeOnce sync.Once
		// shieldedServices 记录已经报过“本地路由优先”护盾的服务名（见 store）。
		// refresh 每 60s 重跑，不去重就是每分钟一屏同样的告警。
		shieldedServices sync.Map
	}
)

// Validate checks if an endpoint is valid for being served.
// 仅校验基础字段；早期注释掉的 POSIX 正则路径校验未保留，因为当前路由
// 使用 radix tree 而非 POSIX 正则，path 形如 "/foo/:bar" 而非 "^/foo/(\w+)$"。
func Validate(e *registry.Endpoint) error {
	if e == nil {
		return errors.New("endpoint is nil")
	}
	if e.Name == "" {
		return errors.New("endpoint name required")
	}
	if e.Path == "" {
		return errors.New("endpoint path required")
	}
	return nil
}

// New initializes and returns a new router instance.
// NOTE: Router initialization is not fully thread-safe; multi-server calls are not recommended during startup.
func New(opts ...Option) *TRouter {
	cfg := newConfig(opts...)
	router := &TRouter{
		TGroup: NewGroup(
			WithStatic(), // Enable static file support by default.
		),
		config:     cfg,
		middleware: newMiddlewareManager(),
		exit:       make(chan bool),
	}
	cfg.Router = router
	router.TGroup.config.StaticCacheTTL = cfg.StaticCacheTTL

	// registry 每 60s 刷新（watch/refresh→store）会为同一 (method,path) 生成
	// **新 id** 的 route 对象热替换树节点；上下文池按 route id 键控、请求命中时
	// 惰性创建，若不随替换清理，被命中过的旧 id 池条目会随刷新无限累积。
	// 回调在 tree 写锁内执行，只做 sync.Map 删除，无锁交叉。
	router.tree.onRouteReplaced = func(old *route) {
		router.httpCtxPool.Delete(old.Id())
		router.rpcCtxPool.Delete(old.Id())
	}

	go router.watch()   // Real-time registry subscription.
	go router.refresh() // Periodic registry refreshing.
	go func() {
		<-router.exit
		StopAllStaticStores()
	}()

	return router
}

func (self *TRouter) PrintRoutes() {
	if self.Config().RouterTreePrinter {
		self.tree.PrintTrees()
	}
}

// Config implements IRouter.Config.
func (self *TRouter) Config() *Config {
	return self.config
}

// String implements IRouter.String.
func (self *TRouter) String() string {
	return "volts-router"
}

// Handler implements IRouter.Handler.
func (self *TRouter) Handler() interface{} {
	return self
}

// Register adds an endpoint to the router tree after validation.
func (self *TRouter) Register(ep *registry.Endpoint) error {
	if err := Validate(ep); err != nil {
		return err
	}
	return self.tree.AddRoute(EndpiontToRoute(ep))
}

// Deregister removes an endpoint and cleans up associated context pools.
func (self *TRouter) Deregister(ep *registry.Endpoint) error {
	path := ep.Metadata["path"]
	r := EndpiontToRoute(ep)

	// Clean up pool entries for the existing route before deletion.
	// Route IDs are auto-incrementing; we must match the stored route
	// in the tree to get the correct ID for pool cleanup.
	for _, method := range ep.Method {
		if existing, _ := self.tree.Match(method, path); existing != nil {
			self.httpCtxPool.Delete(existing.Id())
			self.rpcCtxPool.Delete(existing.Id())
		}
	}

	return self.tree.DelRoute(path, r)
}

// Endpoints returns a map of all registered endpoints grouped by their TGroup.
func (self *TRouter) Endpoints() (services map[*TGroup][]*registry.Endpoint) {
	var subscriberList []ISubscriber

	for e := range self.subscribers {
		// Only advertise non-internal subscribers.
		if !e.Config().Internal {
			subscriberList = append(subscriberList, e)
		}
	}
	// Sort subscribers by topic name for consistency.
	sort.Slice(subscriberList, func(i, j int) bool {
		return subscriberList[i].Topic() > subscriberList[j].Topic()
	})

	eps := self.tree.Endpoints()
	for grp, endpoints := range eps {
		for _, e := range subscriberList {
			endpoints = append(endpoints, e.Endpoints()...)
		}
		eps[grp] = endpoints
	}

	return eps
}

// RegisterMiddleware adds transformation functions that create middlewares.
func (self *TRouter) RegisterMiddleware(middlewares ...func(IRouter) IMiddleware) {
	for _, creator := range middlewares {
		middleware := creator(self)
		// Register by name if the middleware implements IMiddlewareName, otherwise use type string.
		if mm, ok := middleware.(IMiddlewareName); ok {
			self.middleware.Add(mm.Name(), creator)
		} else {
			typ := reflect.TypeOf(middleware)
			if typ.Kind() == reflect.Ptr {
				typ = typ.Elem()
			}
			name := typ.String()
			self.middleware.Add(name, creator)
		}
	}
}

// Use 把中间件挂到全局链，对**所有**路由生效 —— 包括之后新增的路由。
//
// 这是与 RegisterMiddleware 的关键分工：RegisterMiddleware 只把构造器登记进
// 名字表，真正生效仍要靠控制器内嵌，漏内嵌就静默没有；Use 则是默认覆盖，
// 适合 CORS / 鉴权 / 限流 / 追踪这类"漏一条路由就是事故"的横切关注点。
//
// 单入口即可同时服务 HTTP 与 RPC —— 适配性由中间件自身的类型携带，
// 在 handler.init 按 TransportType 筛选，不适用的一侧 funcs 里根本不存在
// 该条目，请求路径零开销：
//
//	Use(func(*THttpContext))          // 仅 HTTP，RPC 链自动跳过
//	Use(func(*TRpcContext))           // 仅 RPC，HTTP 链自动跳过
//	Use(func(IContext))               // 协议无关，两侧都装
//	Use(func(IRouter) IMiddleware)    // 由中间件实现 IMiddlewareTransport 自行声明，
//	                                  // 未声明则两侧都装
//
// 执行顺序：全局链 → 路由级 mids → 控制器内嵌中间件 → 主控制器。
//
// NOTE 生效时机：中间件在 handler 首次初始化（首个请求）时装配，因此 Use 与
// Url 的先后无所谓；但**服务开始处理请求后再 Use 不会影响已初始化的 handler**。
// 请在启动前完成全部 Use 调用。
func (self *TRouter) Use(middlewares ...any) {
	if len(middlewares) == 0 {
		return
	}

	self.Lock()
	defer self.Unlock()
	self.globalMiddlewares = append(self.globalMiddlewares, middlewares...)
}

// Middlewares 返回全局链的快照。
// 返回副本而非底层切片 —— 调用方（handler.init）会在自己的锁里持有它，
// 共享底层数组会让后续 Use 的 append 与之竞争。
func (self *TRouter) Middlewares() []any {
	self.RLock()
	defer self.RUnlock()

	if len(self.globalMiddlewares) == 0 {
		return nil
	}
	return append([]any(nil), self.globalMiddlewares...)
}

// register module
func (self *TRouter) RegisterGroup(groups ...IGroup) {
	self.Lock()
	defer self.Unlock()

	for _, group := range groups {
		//group := group
		self.tree.Conbine(group.GetRoutes())

		for sub, lst := range group.GetSubscribers() {
			if rawLst, has := self.TGroup.subscribers[sub]; !has {
				self.TGroup.subscribers[sub] = lst
			} else {
				/*
					var news []broker.ISubscriber
					// 排除重复的
					for _, sb := range lst {
						for _, s := range rawLst {
							if s.Topic() == sb.Topic() {
								goto next
							}
						}
						news = append(news, sb)
					next:
					}*/

				self.TGroup.subscribers[sub] = append(rawLst, lst...)
			}
		}
	}
}

// ServeHTTP implements the http.Handler interface, dispatching requests to HTTP or RPC handlers.
func (self *TRouter) ServeHTTP(w http.ResponseWriter, r *transport.THttpRequest) {
	// Log the request path if enabled.
	if self.config.RequestPrinter {
		defer func() {
			log.Infof("[Path]%v", r.URL.Path)
		}()
	}

	if r.Method == "CONNECT" {
		// Serve as a raw network/TCP connection for RPC.
		hj, ok := w.(http.Hijacker)
		if !ok {
			log.Errf("rpc hijacking: ResponseWriter does not implement http.Hijacker, addr=%s", r.RemoteAddr)
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			// Hijack 失败时 conn 为 nil，继续走下面的 io.WriteString(conn,...)
			// 必然 nil deref panic —— 此前就是这么写的。必须直接返回。
			log.Errf("rpc hijacking %v: %v", r.RemoteAddr, err.Error())
			return
		}
		// 一旦 Hijack 成功，连接生命周期归我们管，http.Server 不再负责关闭它。
		io.WriteString(conn, "HTTP/1.0 200 Connected to RPC\n\n")

		msg, err := transport.ReadMessage(conn)
		if err != nil {
			// 读不到首帧就无从构造 RpcRequest（msg 可能为 nil），继续会带着坏
			// 消息往下走。已劫持的连接必须显式关闭，否则这条 socket 泄漏。
			log.Errf("rpc Read %s", err.Error())
			conn.Close()
			return
		}
		sock := transport.NewTcpTransportSocket(conn, 0, 0)
		req := transport.NewRpcRequest(r.Context(), msg, sock)
		rsp := transport.NewRpcResponse(r.Context(), req, sock)
		self.ServeRPC(rsp, req)
	} else {
		// Serve as a standard web/HTTP request.
		var rsp *transport.THttpResponse
		if v := self.respPool.Get(); v == nil {
			rsp = transport.NewHttpResponse(r.Context(), r)
		} else {
			rsp = v.(*transport.THttpResponse)
		}
		rsp.Connect(w)

		// 路由匹配前的路径改写(多语言 URL 前缀:/fr/shop → /shop,前缀记为 "/fr")。
		// 必须在 tree.Match 之前——middleware 是装配期挂到 handler 链上的，匹配已经
		// 发生过了，那时再改路径没有意义。摘下来的前缀经 ctx.PathPrefix() 交给
		// handler，重定向与链接生成由上层拼回去。见 PathRewriter 的类型注释。
		var pathPrefix string
		if self.config.PathRewriter != nil {
			if newPath, stripped := self.config.PathRewriter(r.URL.Path); stripped != "" && newPath != "" {
				pathPrefix = stripped
				r.URL.Path = newPath
			}
		}

		// Match route from the internal tree.
		route, params := self.tree.Match(r.Method, r.URL.Path)

		// 尾斜杠规范化：`/shop/checkout/` 与 `/shop/checkout` 是同一个页面，但框架
		// 从不处理尾斜杠——**每条路由都如此**，而地址栏、书签、外链、爬虫随手补一个
		// 斜杠是常态。
		//
		// 症状比"404"更隐蔽：带尾斜杠的路径通常**不是没匹配上**，而是掉进了同层的
		// 通配路由。真栈实测 `/shop/checkout/` 被 `/shop/{slug}` 接走，商品页拿
		// "checkout/" 当 slug 解不出 id，回一句 "product not found" ——
		// 页面明明注册着，用户看到的却是"这个商品不存在"。
		//
		// 判据不是"有没有匹配上"，而是**去掉尾斜杠后是不是落到了另一条路由**：
		//   - 落到另一条（通配 → 静态）  → 说明尾斜杠让它跑偏了，跳到规范写法；
		//   - 还是同一条（如 /static/a/ 与 /static/a 同属一个通配路由）→ 不动，
		//     否则每个以斜杠结尾的通配路径都会被无谓地跳一次；
		//   - 两边都没有                → 老老实实 404。
		// 只做"去掉"这一个方向：注册侧本来就会把 `/docs/` 规范成 `/docs`，反方向
		// 再跳会让 `/shop` 被弹去 `/shop/`（那是另一条通配路由）。
		if len(r.URL.Path) > 1 && strings.HasSuffix(r.URL.Path, "/") {
			if trimmed := strings.TrimRight(r.URL.Path, "/"); trimmed != "" {
				alt, _ := self.tree.Match(r.Method, trimmed)
				if alt != nil && (route == nil || alt.Id() != route.Id()) {
					u := *r.URL
					// 前缀必须拼回去：PathRewriter 摘掉的是语言前缀，跳到去前缀的
					// 路径会把语言静默丢掉——`/fr/shop/checkout/` 跳去 `/shop/checkout`
					// 后用户看到的是默认语言，而且这条 301 会被缓存。
					u.Path = pathPrefix + trimmed
					// GET/HEAD 用 301（可缓存的永久跳转，惯例）；其余方法用 308 ——
					// 308 才保证客户端**不把 POST 改写成 GET**，否则带表单的请求会
					// 静默丢掉 body 变成一次 GET，用户看到的是"提交没反应"。
					code := http.StatusMovedPermanently
					if r.Method != http.MethodGet && r.Method != http.MethodHead {
						code = http.StatusPermanentRedirect
					}
					w.Header().Set("Location", u.RequestURI())
					w.WriteHeader(code)
					rsp.ResponseWriter = nil
					self.respPool.Put(rsp)
					return
				}
			}
		}

		if route == nil {
			rsp.WriteHeader(http.StatusNotFound)
			// 404 也要把 rsp 归还池——否则扫描器/缺失静态资源刷 404 时，每个
			// 请求都重新分配一个 THttpResponse，池形同虚设。
			rsp.ResponseWriter = nil
			self.respPool.Put(rsp)
			return
		}

		// Retrieve or create a context pool for this route.
		p, _ := self.httpCtxPool.LoadOrStore(route.Id(), &sync.Pool{New: func() interface{} {
			return NewHttpContext(self)
		}})

		pool, ok := p.(*sync.Pool)
		if !ok {
			// 不可能发生：本包内 httpCtxPool 只存 *sync.Pool。防御性守卫，避免
			// 未来若有人误存其他类型时直接 panic 拉崩整个 HTTP server。
			log.Errf("httpCtxPool corruption: unexpected type %T for route %v", p, route.Id())
			rsp.WriteHeader(http.StatusInternalServerError)
			rsp.ResponseWriter = nil
			self.respPool.Put(rsp)
			return
		}
		ctx := pool.Get().(*THttpContext)
		ctx.route = route
		if !ctx.inited {
			ctx.router = self
			ctx.inited = true
			ctx.Template = template.Default()
		}
		ctx.reset(rsp, r)
		ctx.pathPrefix = pathPrefix // reset 之后赋值：reset 会清掉它
		ctx.setPathParams(params)

		// Execute the routing chain.
		self.route(route, ctx)

		// 连接被接管（WebSocket 升级）后，处理方的 goroutine 仍持有 ctx 与 rsp，
		// 且底层 socket 已脱离 http.Server 管理。此时既不能再写响应，也绝不能
		// 归还池——否则下一个请求会取到同一个对象，与 WS goroutine 同时读写。
		if ctx.hijacked {
			return
		}

		// Apply the context changes and return objects to pools.
		ctx.Apply()
		pool.Put(ctx)
		rsp.ResponseWriter = nil
		self.respPool.Put(rsp)
	}
}

// ServeRPC handles RPC requests, including heartbeat packets and method dispatching.
func (self *TRouter) ServeRPC(w *transport.RpcResponse, r *transport.RpcRequest) {
	reqMessage := r.Message
	// Handle heartbeat packets immediately.
	if reqMessage.IsHeartbeat() {
		reqMessage.SetMessageType(transport.MT_RESPONSE)
		data := reqMessage.Encode()
		w.Write(data)
		return
	}

	//resMetadata := make(map[string]string)
	//newCtx := context.WithValue(context.WithValue(ctx, share.ReqMetaDataKey, req.Metadata),
	//	share.ResMetaDataKey, resMetadata)

	//		ctx := context.WithValue(context.Background(), RemoteConnContextKey, conn)
	//serviceName := reqMessage.Header["ServicePath"]
	//methodName := msg.ServiceMethod
	st := reqMessage.SerializeType()
	//res := transport.GetMessageFromPool()
	//res.SetMessageType(transport.MT_RESPONSE)
	//res.SetSerializeType(st)
	// 获取支持的序列模式
	//
	// 必须走 IdentifyWireCodec：st 是**对端在协议头里填的一个字节**，任何能连上
	// 端口的人都能借此指定服务端用哪个解码器去解析他发来的数据。默认受限的
	// codec（如 Gob，解不可信输入有内存放大风险）在此被拒。
	coder := codec.IdentifyWireCodec(st)
	if coder == nil {
		w.WriteHeader(transport.StatusForbidden)
		w.Write([]byte{})
		return
	}

	// Match the route for the RPC path.
	route, params := self.tree.Match("CONNECT", reqMessage.Path)
	if route == nil {
		w.WriteHeader(transport.StatusNotFound)
		w.Write([]byte{})
		return
	} else {
		// Initialize or retrieve the RPC context pool.
		p, _ := self.rpcCtxPool.LoadOrStore(route.Id(), &sync.Pool{New: func() interface{} {
			return NewRpcHandler(self)
		}})

		pool, ok := p.(*sync.Pool)
		if !ok {
			log.Errf("rpcCtxPool corruption: unexpected type %T for route %v", p, route.Id())
			w.WriteHeader(transport.StatusInternalError)
			w.Write([]byte{})
			return
		}
		ctx := pool.Get().(*TRpcContext)
		if !ctx.inited {
			ctx.router = self
			ctx.route = route
			ctx.inited = true
		}
		ctx.reset(w, r, self, route)
		ctx.setPathParams(params)

		// 把 wire 帧头灌回 context metadata。
		//
		// 客户端侧 rpc_client 已经把 metadata.FromContext(ctx) 与 req.Header() 一起写进
		// 帧头；服务端这一步把它还原，于是横切信息（调用方身份、租户上下文、追踪 id）
		// 能穿透到任意深度的业务代码，而不必一路手动传参 —— 与 gRPC 的 metadata 同义。
		//
		// 全量灌入而不做白名单过滤：过滤名单是又一个会漂移的东西，而"帧头即 metadata"
		// 这条规则可预测。ServicePath/ServiceMethod 等保留键同样在内，它们另有
		// r.Service()/r.Method() 可取，重复不构成问题。
		//
		// 复用安全性依赖 TRpcContext.reset 清空 self.context（见 rpc.go 里那段注释）：
		// 不清就会把上一个请求的身份串给下一个。
		if len(reqMessage.Header) > 0 {
			base := r.Context
			if base == nil {
				base = context.Background()
			}
			ctx.Context(metadata.MergeContext(base, reqMessage.Header, true))
		}

		// Execute the controller.
		self.route(route, ctx)

		pool.Put(ctx) // Pool 回收 Handler
	}

	// 返回数据
	if !reqMessage.IsOneway() {
		// 序列化数据
		/* remove 已经交由Body response处理
		data, err := coder.Encode(ctx.replyv.Interface())
		//argsReplyPools.Put(mtype.ReplyType, replyv)
		if err != nil {
			handleError(res, err)
			return
		}
		res.Payload = data

		if len(resMetadata) > 0 { //copy meta in context to request
			meta := res.Header
			if meta == nil {
				res.Header = resMetadata
			} else {
				for k, v := range resMetadata {
					meta[k] = v
				}
			}
		}

		err = w.Write(res.Payload)
		if err != nil {
			log.Dbg(err.Error())
		}
		log.Dbg("aa", string(res.Payload))*/
	}
}

func (self *TRouter) route(route *route, ctx IContext) {
	if self.config.Recover {
		defer func() {
			if err := recover(); err != nil {
				log.Err(err)

				if self.config.RecoverHandler != nil {
					self.config.RecoverHandler(ctx)
				}
			}
		}()
	}

	for _, handlerId := range route.Handlers() {
		// TODO 回收需要特殊通道 直接调用占用了处理时间
		handler := defaultHandlerManager.Get(handlerId)
		if handler == nil {
			log.Errf("Handler %d not found", handlerId)
			continue
		}

		self.invokeHandler(handlerId, handler, ctx)
	}
}

// invokeHandler 执行单个 handler，并保证无论如何都把它归还给对象池。
//
// 单独成函数是为了拿到一个 defer 作用域：此前 reset + Put 写在调用之后，
// handler panic 时这两步被直接跳过 —— 该 handler 对象永远回不了池，
// 下一个请求只能重新 clone 模型并走一遍 init（反射建控制器、注入中间件）。
// 恰恰是"业务在 panic"这种最需要省资源的时刻，代价被放大。
func (self *TRouter) invokeHandler(id int, handler *handler, ctx IContext) {
	defer func() {
		// reset 会遍历 funcs 调各中间件的 Reset()。若此处再 panic，会把正在
		// 传播的原始 panic 顶替掉，日志里只剩下无关的二次现场 —— 原始堆栈
		// 才是排查依据，必须保住。
		defer func() {
			if r := recover(); r != nil {
				log.Errf("handler %s 回收时 panic（原始错误可能已被覆盖）: %v", handler.Name, r)
			}
		}()

		handler.reset()
		defaultHandlerManager.Put(id, handler)
	}()

	if !handler.inited {
		handler.init(self)
	}

	handler.Invoke(ctx)
}

func (self *TRouter) isClosed() bool {
	select {
	case <-self.exit:
		return true
	default:
		return false
	}
}

// Close 停止路由器的后台任务（registry watch/refresh、静态存储清理）。幂等。
// 由 server 的 Stop 路径调用——此前 exit 通道从无人关闭，这些 goroutine 在
// 服务器停止后仍永久运行并继续轮询 registry。
func (self *TRouter) Close() {
	self.closeOnce.Do(func() { close(self.exit) })
}

// filteSelf filters out the nodes that belong to the current instance from a service.
// selfIdentity 是本进程注册出去的全部节点的身份集合，filteSelf 用它把发现结果里
// 指向自身的节点剔除，避免建出自环反代路由（会把本地 handler 覆盖成打向自身）。
//
// 同时按两条线匹配：
//   - node.Id：常规情形，本进程注册的节点 id。
//   - 归一化地址（addr.LocalFormat 后的 host:port）：抓住 id 对不上但地址相同的
//     情形 —— 多进程用 server.Debug() 撞同一端口时，撞端口的节点 id 是别的进程
//     的、地址却落在本机监听端口上，纯 node.Id 匹配结构性漏掉，只有地址线能识别。
//     归一化与反代拨号路径（reverse_proxy.go 的 addr.LocalFormat）用同一把尺子，
//     因此“会被拨到哪”和“算不算自己”判定一致。
type selfIdentity struct {
	ids   map[string]struct{}
	addrs map[string]struct{}
}

// localIdentity 从 RegistryCacher.LocalServices()（本进程的注册记录，非 etcd 镜像
// 缓存，因此不随 lease 抖动）收集自身身份。返回 nil 表示本地服务尚未注册上来、
// 暂时无法确定“自己是谁”，由调用方 store() 决定护盾还是放行。
func (self *TRouter) localIdentity() *selfIdentity {
	locals := self.config.RegistryCacher.LocalServices()
	if len(locals) == 0 {
		return nil
	}

	me := &selfIdentity{
		ids:   make(map[string]struct{}),
		addrs: make(map[string]struct{}),
	}
	for _, srv := range locals {
		if srv == nil {
			continue
		}
		for _, n := range srv.Nodes {
			if n == nil {
				continue
			}
			me.ids[n.Id] = struct{}{}
			me.addrs[addr.LocalFormat(n.Address)] = struct{}{}
		}
	}
	return me
}

// filteSelf 返回一个副本，其中指向本进程自身的节点已被剔除（按 node.Id 或归一化
// 地址匹配），防止转发时无限自环。me 必须非 nil —— 自身未知的护盾/放行分支由调用方
// store() 提前处置，不进这里。
func (self *TRouter) filteSelf(srv *registry.Service, me *selfIdentity) *registry.Service {
	service := *srv // Shallow copy to avoid mutating the cached original service object

	nodes := make([]*registry.Node, 0, len(service.Nodes))
	for _, n := range service.Nodes {
		if n == nil {
			continue
		}
		// node.Id 或归一化地址任一命中即视为自身，剔除以防无限自环。
		if _, hit := me.ids[n.Id]; hit {
			continue
		}
		if _, hit := me.addrs[addr.LocalFormat(n.Address)]; hit {
			continue
		}

		nodes = append(nodes, n)
	}

	service.Nodes = nodes
	return &service
}

// filteHttpNodes 返回一个副本，其中**收不了 HTTP 的节点**已被剔除。
//
// 同一个服务名下常同时挂着 HTTP 与 RPC 两种节点：vectors 的 http/rpc 两个 server
// 共用一个 router，于是各自按自己的监听端口注册同名服务，且**两条注册记录带的是
// 同一份完整端点表**（HTTP 路由 + CONNECT 路由都在里面）。当发现结果里只剩下 RPC
// 节点时（filteSelf 刚把 HTTP 节点当自己剔掉、或缓存里那一版只收到 rpc 键的事件），
// 老逻辑照样会按表里的 HTTP 端点建出代理路由 —— 而请求真到来时
// reverse_proxy.getService 只在 protocol=http/https 的节点里选，选回来的正是本进程
// 自己的 HTTP 端口，于是 508 自环，本地路由还已经被这条代理路由顶掉了。
//
// 判定取宽：只剔除**明确声明了非 http 协议**的节点。没有 metadata 的节点（测试、
// 老注册记录、别的注册中心后端）一律保留，避免把本来能用的代理路由一起挡掉。
func filteHttpNodes(srv *registry.Service) *registry.Service {
	service := *srv // 浅拷贝，勿改缓存里的原对象

	nodes := make([]*registry.Node, 0, len(service.Nodes))
	for _, n := range service.Nodes {
		if n == nil {
			continue
		}
		if p, has := n.Metadata["protocol"]; has && p != "" {
			switch strings.ToLower(p) {
			case "http", "https":
			default:
				continue
			}
		}
		nodes = append(nodes, n)
	}

	service.Nodes = nodes
	return &service
}

// hasLocalRoute 判断 (methods, path) 是否已由本进程的**本地** handler 提供。
// 按路由模式串精确查（不是拿请求路径去匹配通配），因此只有"同一条路由"才算命中。
// 认不出 handler 类型时按本地处理：宁可少建一条代理路由，也不冒覆盖本地 handler 的险。
func (self *TRouter) hasLocalRoute(methods []string, path string) bool {
	for _, m := range methods {
		r := self.tree.RouteOf(m, path)
		if r == nil {
			continue
		}
		for _, id := range r.Handlers() {
			if h := defaultHandlerManager.Model(id); h == nil || h.Type != ProxyHandler {
				return true
			}
		}
	}
	return false
}

// store maps registry services to internal endpoints.
func (self *TRouter) store(services []*registry.Service) {
	if len(services) == 0 {
		return
	}

	// 收集本进程的“自身身份”（node.Id + 归一化地址），用于剔除指向自己的节点。
	me := self.localIdentity()

	// 不知道“自己是谁”就**一律不建代理路由**。
	//
	// 此前这里分两支：已挂本地路由 → 护盾跳过；尚无本地路由 → 认为“纯网关刚启动、
	// 无自身可环”而**放行全部节点**。后一支的前提不成立，是一个真实的启动竞态：
	// 进程注册自己的服务（写进 registry）与 RegistryCacher.LocalServices() 可见、
	// 以及模块把本地路由挂上路由树，这三件事并不同步。refresh() 首轮只延迟 5s 就跑，
	// 完全可能落在“自己的服务已经在注册中心里、但 LocalServices() 还是空、路由树也
	// 还没挂上本地路由”的窗口里 —— 于是本进程的服务被当成远程服务，按它的 endpoints
	// 建出一整批**指向自身**的代理路由。
	//
	// 后果不是“慢一拍”而是永久性的：TTree.addNode 对同路径是后写覆盖，这些自环路由
	// 会顶掉随后挂上来的本地 handler；而 refresh() 的后续轮次会把该服务判成 local 并
	// 跳过（isLocalService），**再也不会去修正已经写进树里的坏路由**。
	//
	// 2026-07-26 真栈实测：kylin 同进程托管 account，`/account/static/*` 对存在的文件
	// 稳定返回 508（`http: proxy self-loop blocked ... Target: http://127.0.0.1:35999
	// == own listen`），而对不存在的文件返回 404 —— 说明本地静态路由建对了、只是被
	// 自环代理路由盖住。直接后果是装模块时取不到 Data 文件
	// （core/download 用 /{service}/static/... 下载），**依赖闭包含该服务的租户一律
	// 建不出来**。同进程的其它模块没暴露，只是因为它们把业务端点登记成 CONNECT、
	// HTTP 端点很少（account 的 http 节点有 161 个 endpoint，purchase 只有 2 个），
	// 并非 filteSelf 对它们生效了。
	//
	// 代价：识别不出自身的那一轮不更新代理路由，60s 后下一轮重来。这与
	// isLocalService 那里“宁可假阴性、多跑一轮 store()”是同一个取舍方向 ——
	// 少一轮路由更新是可恢复的，建出自环路由则不可恢复。
	if me == nil {
		return
	}

	// Create a new endpoint mapping.
	for _, service := range services {
		// me 到这里必非 nil（上面已提前返回），无需再判空。
		service = self.filteSelf(service, me)
		// 只有能收 HTTP 的节点才配得上 HTTP 代理路由（见 filteHttpNodes）。
		service = filteHttpNodes(service)
		if len(service.Nodes) > 0 {
			var handlerId int
			var shielded int // 因本地已有路由而跳过的端点数
			for _, sep := range service.Endpoints {
				url := &TUrl{
					Path: sep.Path,
				}
				r := EndpiontToRoute(sep)

				// Determine the handler type (HTTP or RPC/CONNECT).
				isConnect := utils.IndexOf("CONNECT", sep.Method...) != -1
				if isConnect {
					// ★ 不为 RPC 端点建代理路由：RPC 层根本没有反向代理实现
					// （RpcReverseProxy 是空函数体）。这条路由唯一的效果是把请求
					// 静默吞掉 —— 调用方等满 RequestTimeout 才拿到
					// "context deadline exceeded"；更糟的是 TTree.addNode 对同路径
					// 路由是「后写覆盖」，它会顶掉本地真正的 RPC handler，把本来
					// 正常的本地调用也变成黑洞。
					//
					// 跨进程 RPC 本就由 client 按注册中心直连目标节点，从不经网关
					// 转发，所以这里没有任何东西需要代理。不建路由后，请求打到没有
					// 该服务的进程会立刻得到 StatusNotFound，而不是挂到超时。
					continue
				}

				// ★ 本地路由优先：这条路径本进程自己就在服务，绝不拿发现来的代理
				// 路由去盖它。
				//
				// TTree.addNode 对同路径是**后写覆盖**，而代理路由永远晚于本地路由
				// （本地路由在模块 init 时挂树，代理路由要等 registry watch/refresh），
				// 所以一旦某条本地路径被判成"远程服务的端点"，本地 handler 就被顶掉，
				// 且 refresh() 之后会把该服务判成 local 直接跳过，**再没人来纠正**。
				// 前面的 filteSelf 只在"能认出自己"时管用，而认自己这件事依赖
				// RegistryCacher.LocalServices() 的填充时机（注册中心先落盘、
				// localServices 后更新；同进程 http/rpc 两个 server 各持一个 cacher，
				// 彼此的节点互相不可见）——真栈上确实漏过：kylin 自己托管的
				// gamification/rating 拿到了指向本进程 :35999 的代理路由，
				// `/gamification/static/security/*.xml` 恒 508，模块升级取不到数据文件
				// （core/download 走 /{service}/static/...），整个升级失败。
				//
				// 这条护盾与"认不认得出自己"无关：本进程能自己服务的路径，转发出去
				// 在任何拓扑下都不会更对。
				if self.hasLocalRoute(sep.Method, sep.Path) {
					shielded++
					continue
				}

				handlerId = generateHandler(ProxyHandler, HttpHandler, []interface{}{HttpReverseProxy}, nil, url, service.Name).Id

				r.handlers = append(r.handlers, handlerId)
				if err := self.tree.AddRoute(r); err != nil {
					log.Err(err)
				}
			}

			// 每个服务只报一次：refresh 每 60s 重跑一轮，同样的护盾命中会一直重复，
			// 天天刷屏的告警等于没有告警。首次仍要报 —— 它说明本进程没认出自己的
			// 注册记录（filteSelf 漏了），是一个值得查的拓扑/时序信号。
			if shielded > 0 {
				if _, dup := self.shieldedServices.LoadOrStore(service.Name, struct{}{}); !dup {
					log.Warnf("router: 服务 %q 有 %d 个端点已由本进程的本地路由提供，未建代理路由。"+
						"发现结果里混进了本进程自己的节点而 filteSelf 没认出来 —— 常见于同一个 router 挂在多个 server 上"+
						"（http/rpc 各持一个 RegistryCacher，彼此的注册记录互相不可见）。"+
						"本地路由已保住；若这些路径本该转发到别的进程，请检查注册中心里的节点归属",
						service.Name, shielded)
				}
			}
		}
	}
}

// watch continuously monitors endpoint changes in the Registry.
// It ensures real-time updates to the routing tree so requests reach the correct service instances.
func (self *TRouter) watch() {
	var attempts int

	// Delay monitoring for 5 seconds to avoid initial system noise.
	select {
	case <-time.After(5 * time.Second):
	case <-self.exit:
		return
	}

	for {
		if self.isClosed() {
			break
		}

		// Obtain a watcher for registry changes.
		w, err := self.config.Registry.Watcher()
		if err != nil {
			attempts++
			if attempts > 10 {
				attempts = 10
			}
			log.Errf("error watching endpoints: %v", err)
			time.Sleep(time.Duration(attempts) * time.Second)
			continue
		}

		// Wait if no watcher is available.
		if w == nil {
			select {
			case <-time.After(60 * time.Second):
			case <-self.exit:
				return
			}
			continue
		}

		// Create a context with cancellation to manage the watcher goroutine.
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		igoroutine.Go(func() {
			defer close(done)
			select {
			case <-ctx.Done():
				w.Stop()
			case <-self.exit:
				w.Stop()
				cancel()
			}
		}, func(err error) { log.Err(err.Error()) })

		attempts = 0

		for {
			res, err := w.Next()
			if err != nil {
				log.Errf("error getting next endpoint: %v", err)
				cancel()
				break
			}
			if res == nil || res.Service == nil {
				continue
			}

			// Fetch full service information from the cache after a change update.
			services, err := self.config.RegistryCacher.GetService(res.Service.Name)
			if err != nil {
				if errors.Is(err, registry.ErrNotFound) {
					// 服务的最后一个节点注销后，delete 事件紧跟着这次 GetService，
					// 「找不到」正是这条事件的正常结局，不是故障。此前按 ERR 记、
					// 且不带服务名 —— 每次重启一个兄弟进程就刷出一排看不出主语的
					// 红字，真正的故障混在里面反而没人看。
					log.Infof("router: service %q is gone from the registry (watch action=%s)", res.Service.Name, res.Action)
				} else {
					log.Errf("router: unable to get service %q after watch action=%s: %v", res.Service.Name, res.Action, err)
				}
				continue
			}

			// Update local endpoints.
			self.store(services)
		}

		cancel()
		<-done // Wait for the goroutine to finish.
	}
}

// refresh periodically pulls the full service list from the registry.
func (self *TRouter) refresh() {
	// Delay monitoring for 5 seconds to avoid initial system noise.
	select {
	case <-time.After(5 * time.Second):
	case <-self.exit:
		return
	}

	var (
		err      error
		services []*registry.Service
		list     []*registry.Service
		attempts int
		maxRetry = 10
	)

	for {
		// 每轮重新取本地服务快照。此前它在循环外只取一次 —— refresh 启动仅延迟
		// 5s，那一刻本进程往往还没把全部服务注册完（模块是逐个注册上来的），
		// 于是这份残缺快照被后续每一轮 60s 刷新永久沿用：已注册但不在快照里的
		// 本地服务会被反复当成远程服务送进 store()。
		localServices := self.config.RegistryCacher.LocalServices()

		list, err = self.config.Registry.ListServices()
		if err != nil {
			attempts++
			log.Warnf("registry unable to list services: %v", err)
			if attempts > maxRetry {
				log.Errf("max retry exceeded for listing services, giving up")
				attempts = 0
			}
			select {
			case <-time.After(time.Duration(attempts) * time.Second):
			case <-self.exit:
				return
			}
			continue
		}

		if len(list) != 0 {
			attempts = 0

			for _, s := range list {
				// Don't process services that are hosted locally.
				if self.isLocalService(s, localServices) {
					continue
				}

				services, err = self.config.RegistryCacher.GetService(s.Name)
				if err != nil {
					if errors.Is(err, registry.ErrNotFound) {
						// ListServices 与 GetService 之间它注销了：下一轮就不在列表里。
						log.Dbgf("router: service %q vanished between list and get", s.Name)
					} else {
						log.Errf("router: unable to get service %q during refresh: %v", s.Name, err)
					}
					continue
				}
				self.store(services)
			}
		}

		// Refresh interval specified in config.
		select {
		case <-time.After(time.Duration(self.config.RouterTreeRefreshInterval) * time.Second):
		case <-self.exit:
			return
		}
	}
}

// isLocalService checks if a service instance is equivalent to any of the local services.
func (self *TRouter) isLocalService(service *registry.Service, locals []*registry.Service) bool {
	for _, local := range locals {
		if local.Equal(service) {
			return true
		}
	}
	return false
}
