package router

import (
	"io"
	"net"
	"net/http"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/volts-dev/template"
	"github.com/volts-dev/utils"
	"github.com/volts-dev/volts/codec"
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
		TGroup
		//
		config *Config
		// 中间件
		middleware  *TMiddlewareManager
		template    *template.TTemplateSet
		respPool    sync.Pool
		httpCtxPool map[int]*sync.Pool //根据Route缓存
		rpcCtxPool  map[int]*sync.Pool
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
		TGroup: *NewGroup(
			WithStatic(), /* 支持静态文件 */
		),
		config:      cfg,
		middleware:  newMiddlewareManager(),
		httpCtxPool: make(map[int]*sync.Pool),
		rpcCtxPool:  make(map[int]*sync.Pool),
		exit:        make(chan bool),
	}
	cfg.Router = router
	//~router.respPool.New = func() interface{} {
	//	return &transport.THttpResponse{}
	//}

	go router.watch()   // 实时订阅
	go router.refresh() // 定时刷新

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
	return self.tree.DelRoute(path, EndpiontToRoute(ep))
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
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			log.Errf("rpc hijacking %v:%v", r.RemoteAddr, ": ", err.Error())
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
		p, has := self.httpCtxPool[route.Id]
		if !has {
			p = &sync.Pool{New: func() interface{} {
				return NewHttpContext(self)
			}}

			self.httpCtxPool[route.Id] = p
		}

		ctx := p.Get().(*THttpContext)
		if !ctx.inited {
			ctx.router = self
			ctx.route = *route // fixme 复制
			ctx.inited = true
			ctx.Template = template.Default()
		}

		ctx.reset(rsp, r)
		ctx.setPathParams(params)
		ctx.route = *route // TODO 优化重复使用

		self.route(route, ctx)

		// 结束Route并返回内容
		ctx.Apply()

		// 回收资源
		p.Put(ctx) // Pool Handler
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
		//w.Write([]byte("can not find codec for " + st.String()))
		w.Write([]byte{})
		return
	}

	route, params := self.tree.Match("CONNECT", reqMessage.Path) // 匹配路由树
	if route == nil {
		w.WriteHeader(transport.StatusNotFound)
		w.Write([]byte{})
		//w.Write([]byte("rpc: can't match route " + serviceName))
		return
	} else {
		// 初始化Context
		p, has := self.rpcCtxPool[route.Id]
		if !has { // TODO 优化
			p = &sync.Pool{New: func() interface{} {
				return NewRpcHandler(self)
			}}
			self.rpcCtxPool[route.Id] = p
		}

		ctx := p.Get().(*TRpcContext)
		if !ctx.inited {
			ctx.router = self
			ctx.route = *route
			ctx.inited = true
		}
		ctx.reset(w, r, self, route)
		ctx.setPathParams(params)

		// 执行控制器
		self.route(route, ctx)
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

	return
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

	// TODO:将所有需要执行的Handler 存疑列表或者树-Node保存函数和参数
	for _, handler := range route.handlers {
		// TODO 回收需要特殊通道 直接调用占用了处理时间
		handler.init(self).Invoke(ctx).recycle()
	}
}

func (self *TRouter) isClosed() bool {
	// 直接检查通道是否已关闭
	_, isOpen := <-self.exit
	return !isOpen
}

// 过滤自己
func (self *TRouter) filteSelf(service *registry.Service) *registry.Service {
	localServices := self.config.Registry.LocalServices()

	nodes := make([]*registry.Node, 0)
	for _, n := range service.Nodes {
		//  TODO 解决监控拉取registry服务器服务列表比LocalServices获取注册的本地早
		if len(localServices) == 0 && self.tree.Count.Load() > 0 {
			break
		}

		for _, curSrv := range localServices {
			node := curSrv.Nodes[0]
			host, port, err := net.SplitHostPort(node.Address)
			if err != nil {
				log.Err(err)
			}

			if n.Id == node.Id {
				goto out
			}

			h, p, err := net.SplitHostPort(n.Address)
			if err != nil {
				log.Err(err)
			}

			// 同个服务器
			if host == h && port == p {
				goto out
			}

		}
		nodes = append(nodes, n)
	out:
	}

	service.Nodes = nodes
	/*
		for _, curSrv := range localServices {
			if curSrv != nil && service.Name == curSrv.Name {
				node := curSrv.Nodes[0]
				host, port, err := net.SplitHostPort(node.Address)
				if err != nil {
					log.Err(err)
				}

				nodes := make([]*registry.Node, 0)
				var node *registry.Node
				for _, n := range service.Nodes {
					if n.Id == node.Id {
						continue
					}

					h, p, err := net.SplitHostPort(n.Address)
					if err != nil {
						log.Err(err)
					}

					// 同个服务器
					if host == h && port == p {
						continue
					}

					nodes = append(nodes, n)
				}
				service.Nodes = nodes
			}
		}
	*/
	return service
}

// store local endpoint
func (self *TRouter) store(services []*registry.Service) {
	// create a new endpoint mapping
	for _, service := range services {
		service = self.filteSelf(service)
		if len(service.Nodes) > 0 {
			// map per endpoint
			for _, sep := range service.Endpoints {
				url := &TUrl{
					Path: sep.Path,
				}
				r := EndpiontToRoute(sep)
				if utils.IndexOf("CONNECT", sep.Method...) != -1 {
					r.handlers = append(r.handlers, generateHandler(ProxyHandler, RpcHandler, []interface{}{RpcReverseProxy}, nil, url, []*registry.Service{service}))
				} else {
					r.handlers = append(r.handlers, generateHandler(ProxyHandler, HttpHandler, []interface{}{HttpReverseProxy}, nil, url, []*registry.Service{service}))
				}

				err := self.tree.AddRoute(r)
				if err != nil {
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
	time.Sleep(5 * time.Second)

	for {
		if self.isClosed() {
			break
		}

		// watch for changes
		w, err := self.config.Registry.Watcher()
		if err != nil {
			attempts++
			log.Errf("error watching endpoints: %v", err)
			//time.Sleep(time.Duration(attempts) * time.Second)
			continue
		}

		// 无监视者等待
		if w == nil {
			time.Sleep(60 * time.Second)
			continue
		}

		ch := make(chan bool)

		go func() {
			select {
			case <-ch:
				w.Stop()
			case <-self.exit:
				w.Stop()
			}
		}()

		// reset if we get here
		attempts = 0

		for {
			// process next event
			res, err := w.Next()
			if err != nil {
				log.Errf("error getting next endoint: %v", err)
				close(ch)
				break
			}

			// skip these things
			if res == nil || res.Service == nil {
				break
			}

			// get entry from cache
			services, err := self.config.RegistryCacher.GetService(res.Service.Name)
			if err != nil {
				log.Errf("unable to get service: %v", err)
				break
			}

			// update our local endpoints
			self.store(services)
		}
	}
}

// refresh list of api services
func (self *TRouter) refresh() {
	// 5秒后才启动监测
	time.Sleep(5 * time.Second)

	var (
		err      error
		services []*registry.Service
		list     []*registry.Service
		attempts int
	)

	for {
		list, err = self.config.Registry.ListServices()
		if err != nil {
			attempts++
			log.Warnf("registry unable to list services: %v", err)
			time.Sleep(time.Duration(attempts) * time.Second)
			continue
		}
		// 无监视者等待
		if len(list) == 0 {
			time.Sleep(60 * time.Second)
			continue
		}

		attempts = 0

		// for each service, get service and store endpoints
		for _, s := range list {
			for _, local := range self.config.Registry.LocalServices() {
				if local.Equal(s) {
					// 不添加自己
					goto out
				}

			}

			services, err = self.config.RegistryCacher.GetService(s.Name)
			if err != nil {
				log.Errf("unable to get service: %v", err)
				continue
			}
			self.store(services)

		out:
		}

		// refresh list in 10 minutes... cruft
		// use registry watching
		select {
		case <-time.After(time.Minute * 10):
		case <-self.exit:
			return
		}
	}
}
