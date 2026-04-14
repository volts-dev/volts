package router

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/volts-dev/template"
	"github.com/volts-dev/utils"
	"github.com/volts-dev/volts/codec"
	igoroutine "github.com/volts-dev/volts/internal/goroutine"
	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/transport"
)

var log = logger.New("router")

type (
	// Router handle serving messages
	IRouter interface {
		Config() *Config // Retrieve the options
		String() string
		Handler() interface{} // 连接入口 serveHTTP 等接口实现
		// Register endpoint in router
		Register(ep *registry.Endpoint) error
		// Deregister endpoint from router
		Deregister(ep *registry.Endpoint) error
		// list all endpiont from router
		Endpoints() (services map[*TGroup][]*registry.Endpoint)
		RegisterMiddleware(middlewares ...func(IRouter) IMiddleware)
		RegisterGroup(groups ...IGroup)
		PrintRoutes()
	}

	// router represents an RPC router.
	TRouter struct {
		sync.RWMutex
		// router is a group set
		*TGroup
		//
		config *Config
		// 中间件
		middleware  *TMiddlewareManager
		template    *template.TTemplateSet
		respPool    sync.Pool
		httpCtxPool sync.Map // 根据Route缓存，使用sync.Map以支持并发和自动清理
		rpcCtxPool  sync.Map // 使用sync.Map替代map[int]*sync.Pool
		// compiled regexp for host and path
		exit chan bool
	}
)

// TODO Validate validates an endpoint to guarantee it won't blow up when being served
func Validate(e *registry.Endpoint) error {
	/*	if e == nil {
			return errors.New("endpoint is nil")
		}

		if len(e.Name) == 0 {
			return errors.New("name required")
		}

		for _, p := range e.Path {
			ps := p[0]
			pe := p[len(p)-1]

			if ps == '^' && pe == '$' {
				_, err := regexp.CompilePOSIX(p)
				if err != nil {
					return err
				}
			} else if ps == '^' && pe != '$' {
				return errors.New("invalid path")
			} else if ps != '^' && pe == '$' {
				return errors.New("invalid path")
			}
		}

		if len(e.Handler) == 0 {
			return errors.New("invalid handler")
		}
	*/
	return nil
}

// 新建路由
// NOTE 路由不支持线程安全 不推荐多服务器调用！
func New(opts ...Option) *TRouter {
	cfg := newConfig(opts...)
	router := &TRouter{
		TGroup: NewGroup(
			WithStatic(), /* 支持静态文件 */
		),
		config:     cfg,
		middleware: newMiddlewareManager(),
		exit:       make(chan bool),
	}
	cfg.Router = router
	router.TGroup.config.StaticCacheTTL = cfg.StaticCacheTTL
	//~router.respPool.New = func() interface{} {
	//	return &transport.THttpResponse{}
	//}

	go router.watch()   // 实时订阅
	go router.refresh() // 定时刷新
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

func (self *TRouter) Config() *Config {
	return self.config
}

func (self *TRouter) String() string {
	return "volts-router"
}

func (self *TRouter) Handler() interface{} {
	return self
}

// registry a endpoion to router
func (self *TRouter) Register(ep *registry.Endpoint) error {
	if err := Validate(ep); err != nil {
		return err
	}
	return self.tree.AddRoute(EndpiontToRoute(ep))
}

func (self *TRouter) Deregister(ep *registry.Endpoint) error {
	path := ep.Metadata["path"]
	r := EndpiontToRoute(ep)

	// 在删除前，先从 tree 中查出已注册路由的真实 ID，用于清理 pool 条目
	// route ID 是自增计数器，每次 EndpiontToRoute 产生不同 ID，
	// 必须通过 tree.Match 获取实际存储的 route ID
	for _, method := range ep.Method {
		if existing, _ := self.tree.Match(method, path); existing != nil {
			self.httpCtxPool.Delete(existing.Id())
			self.rpcCtxPool.Delete(existing.Id())
		}
	}

	return self.tree.DelRoute(path, r)
}

func (self *TRouter) Endpoints() (services map[*TGroup][]*registry.Endpoint) {
	// 注册订阅列表
	var subscriberList []ISubscriber

	for e := range self.subscribers {
		// Only advertise non internal subscribers
		if !e.Config().Internal {
			subscriberList = append(subscriberList, e)
		}
	}
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

// 注册中间件
func (self *TRouter) RegisterMiddleware(middlewares ...func(IRouter) IMiddleware) {
	for _, creator := range middlewares {
		// 新建中间件
		middleware := creator(self)
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

func (self *TRouter) ServeHTTP(w http.ResponseWriter, r *transport.THttpRequest) {
	// 使用defer保证错误也打印
	if self.config.RequestPrinter {
		defer func() {
			log.Infof("[Path]%v", r.URL.Path)
		}()
	}

	if r.Method == "CONNECT" { // serve as a raw network server
		hj, ok := w.(http.Hijacker)
		if !ok {
			log.Errf("rpc hijacking: ResponseWriter does not implement http.Hijacker, addr=%s", r.RemoteAddr)
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			log.Errf("rpc hijacking %v: %v", r.RemoteAddr, err.Error())
		}
		io.WriteString(conn, "HTTP/1.0 200 Connected to RPC\n\n")

		msg, err := transport.ReadMessage(conn)
		if err != nil {
			log.Errf("rpc Read %s", err.Error())
		}
		sock := transport.NewTcpTransportSocket(conn, 0, 0)
		req := transport.NewRpcRequest(r.Context(), msg, sock)
		rsp := transport.NewRpcResponse(r.Context(), req, sock)
		self.ServeRPC(rsp, req)
	} else { // serve as a web server
		// Pool 提供TResponseWriter
		var rsp *transport.THttpResponse
		if v := self.respPool.Get(); v == nil {
			rsp = transport.NewHttpResponse(r.Context(), r)
		} else {
			rsp = v.(*transport.THttpResponse)
		}
		rsp.Connect(w)

		//获得的地址
		// # match route from tree
		route, params := self.tree.Match(r.Method, r.URL.Path)
		if route == nil {
			rsp.WriteHeader(http.StatusNotFound)
			return
		}

		// # get the new context from pool
		p, _ := self.httpCtxPool.LoadOrStore(route.Id(), &sync.Pool{New: func() interface{} {
			return NewHttpContext(self)
		}})

		pool := p.(*sync.Pool)

		ctx := pool.Get().(*THttpContext)
		ctx.route = route // TODO 优化重复使用
		if !ctx.inited {
			ctx.router = self
			ctx.inited = true
			ctx.Template = template.Default()
		}
		ctx.reset(rsp, r)
		ctx.setPathParams(params)

		self.route(route, ctx)

		// 结束Route并返回内容
		ctx.Apply()
		pool.Put(ctx) // Pool Handler
		rsp.ResponseWriter = nil
		self.respPool.Put(rsp) // Pool 回收TResponseWriter
	}
}

func (self *TRouter) ServeRPC(w *transport.RpcResponse, r *transport.RpcRequest) {
	reqMessage := r.Message // return the packet struct
	// 心跳包 直接返回
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
	coder := codec.IdentifyCodec(st)
	if coder == nil {
		w.WriteHeader(transport.StatusForbidden)
		w.Write([]byte{})
		return
	}

	route, params := self.tree.Match("CONNECT", reqMessage.Path) // 匹配路由树
	if route == nil {
		w.WriteHeader(transport.StatusNotFound)
		w.Write([]byte{})
		return
	} else {
		// 初始化Context
		p, _ := self.rpcCtxPool.LoadOrStore(route.Id(), &sync.Pool{New: func() interface{} {
			return NewRpcHandler(self)
		}})

		pool := p.(*sync.Pool)
		ctx := pool.Get().(*TRpcContext)
		if !ctx.inited {
			ctx.router = self
			ctx.route = route
			ctx.inited = true
		}
		ctx.reset(w, r, self, route)
		ctx.setPathParams(params)

		// 执行控制器
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

		if !handler.inited {
			handler.init(self)
		}

		handler.Invoke(ctx)

		// 回收handler
		handler.reset()
		defaultHandlerManager.Put(handlerId, handler)
	}
}

func (self *TRouter) isClosed() bool {
	select {
	case <-self.exit:
		return true
	default:
		return false
	}
}

// 过滤自己
func (self *TRouter) filteSelf(service *registry.Service, localSet map[string]struct{}) *registry.Service {
	// 如果 localSet 为 nil，说明触发了不同步的安全屏蔽条件
	if localSet == nil {
		service.Nodes = nil
		return service
	}

	nodes := make([]*registry.Node, 0, len(service.Nodes))
	for _, n := range service.Nodes {
		// 校验节点 ID 或节点通信 Address 是否为本实例，是则过滤掉，防止出现请求转发的自循环
		if _, hasId := localSet[n.Id]; hasId {
			continue
		}
		//if _, hasAddr := localSet[n.Address]; hasAddr {
		//	continue
		//}
		nodes = append(nodes, n)
	}

	service.Nodes = nodes
	return service
}

// store local endpoint
func (self *TRouter) store(services []*registry.Service) {
	if len(services) == 0 {
		return
	}

	// 提取一次本地服务信息进行 O(1) 预计算，避免在 for 循环中发生 O(N*M) 的反复提取开销
	var localSet map[string]struct{}
	localServices := self.config.Registry.LocalServices()

	// 解决监控拉取 Registry 服务器服务列表比 LocalServices 注册早的问题
	if len(localServices) == 0 && self.tree.Count.Load() > 0 {
		localSet = nil // nil 表示进入暂置空节点状态
	} else {
		localSet = make(map[string]struct{})
		for _, curSrv := range localServices {
			if curSrv == nil {
				continue
			}
			for _, node := range curSrv.Nodes {
				localSet[node.Id] = struct{}{}
				//localSet[node.Address] = struct{}{}
			}
		}
	}

	// create a new endpoint mapping
	for _, service := range services {
		service = self.filteSelf(service, localSet)
		if len(service.Nodes) > 0 {
			// map per endpoint
			var handlerId int
			for _, sep := range service.Endpoints {
				url := &TUrl{
					Path: sep.Path,
				}
				r := EndpiontToRoute(sep)

				// 判定缓存，去除底层的多次 for
				isConnect := utils.IndexOf("CONNECT", sep.Method...) != -1
				if isConnect {
					handlerId = generateHandler(ProxyHandler, RpcHandler, []interface{}{RpcReverseProxy}, nil, url, []*registry.Service{service}).Id

				} else {
					handlerId = generateHandler(ProxyHandler, HttpHandler, []interface{}{HttpReverseProxy}, nil, url, []*registry.Service{service}).Id
				}

				r.handlers = append(r.handlers, handlerId)
				if err := self.tree.AddRoute(r); err != nil {
					log.Err(err)
				}
			}
		}
	}
}

// watch for endpoint changes
func (self *TRouter) watch() {
	var attempts int

	// 5秒后才启动监测
	select {
	case <-time.After(5 * time.Second):
	case <-self.exit:
		return
	}

	for {
		if self.isClosed() {
			break
		}

		// watch for changes
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

		// 无监视者等待
		if w == nil {
			select {
			case <-time.After(60 * time.Second):
			case <-self.exit:
				return
			}
			continue
		}

		//ch := make(chan bool)
		// 创建带取消功能的 context
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})

		igoroutine.Go(func() {
			defer close(done)
			select {
			case <-ctx.Done():
				w.Stop()
			case <-self.exit:
				w.Stop()
				cancel() // Cancel the context when exit signal is received
			}
		}, func(err error) { log.Err(err.Error()) })
		// reset if we get here
		attempts = 0

		for {
			res, err := w.Next()
			if err != nil {
				log.Errf("error getting next endpoint: %v", err)
				cancel()
				break
			}
			// skip these things
			if res == nil || res.Service == nil {
				continue
			}

			// get entry from cache
			services, err := self.config.RegistryCacher.GetService(res.Service.Name)
			if err != nil {
				log.Errf("unable to get service: %v", err)
				continue
			}

			// update our local endpoints
			self.store(services)
		}

		// 确保 watcher goroutine 已经退出
		cancel()
		<-done // Wait for goroutine to finish properly
	}
}

// refresh list of api services
func (self *TRouter) refresh() {
	// 5秒后才启动监测
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
		maxRetry = 10 // 最大重试次数
	)

	localServices := self.config.Registry.LocalServices()

	for {
		list, err = self.config.Registry.ListServices()
		if err != nil {
			attempts++
			log.Warnf("registry unable to list services: %v", err)
			if attempts > maxRetry {
				log.Errf("max retry exceeded for listing services, giving up")
				attempts = 0 // reset to prevent overflow
			}
			select {
			case <-time.After(time.Duration(attempts) * time.Second):
			case <-self.exit:
				return
			}
			continue
		}

		// 无监视者等待
		if len(list) != 0 {
			attempts = 0

			// for each service, get service and store endpoints
			for _, s := range list {
				if self.isLocalService(s, localServices) {
					continue
				}

				services, err = self.config.RegistryCacher.GetService(s.Name)
				if err != nil {
					log.Errf("unable to get service: %v", err)
					continue
				}
				self.store(services)
			}
		}

		// refresh list in 10 minutes... cruft
		// use registry watching
		select {
		case <-time.After(time.Duration(self.config.RouterTreeRefreshInterval) * time.Second):
		case <-self.exit:
			return
		}
	}
}

// isLocalService 判断服务是否是本地服务
func (self *TRouter) isLocalService(service *registry.Service, locals []*registry.Service) bool {
	for _, local := range locals {
		if local.Equal(service) {
			return true
		}
	}
	return false
}
