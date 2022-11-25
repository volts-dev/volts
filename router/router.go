package router

import (
	"errors"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/volts-dev/template"
	"github.com/volts-dev/utils"
	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/transport"
)

var defaultRouter *TRouter
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
		Endpoints() []*registry.Endpoint
		RegisterMiddleware(middlewares ...func() IMiddleware)
		RegisterGroup(grp ...IGroup)
		PrintRoutes()
	}

	// router represents an RPC router.
	TRouter struct {
		sync.RWMutex
		TGroup // router is a group set
		config *Config

		middleware *TMiddlewareManager // 中间件
		template   *template.TTemplateSet
		//objectPool  *pool
		respPool    sync.Pool
		httpCtxPool map[int]*sync.Pool //根据Route缓存
		rpcCtxPool  map[int]*sync.Pool

		// compiled regexp for host and path
		exit chan bool
	}
)

// clone the middleware object
// 克隆interface 并复制里面的指针
func cloneInterfacePtrFeild(model interface{}) reflect.Value {
	model_value := reflect.Indirect(reflect.ValueOf(model)) //Indirect 等同 Elem()
	model_type := reflect.TypeOf(model).Elem()              // 返回类型
	new_model_value := reflect.New(model_type)              //创建某类型
	new_model_value.Elem().Set(model_value)
	/*
		for i := 0; i < model_value.NumField(); i++ {
			lField := model_value.Field(i)
			Warn("jj", lField, lField.Kind())
			if lField.Kind() == reflect.Ptr {
				//fmt.Println("jj", lField, lField.Elem())
				//new_model_value.Field(i).SetPointer(unsafe.Pointer(lField.Pointer()))
				new_model_value.Elem().Field(i).Set(lField)
				//new_model_value.FieldByName("Id").SetString("fasd")
			}
		}
	*/
	//fmt.Println(new_model_value)
	//return reflect.Indirect(new_model_value).Interface()
	return new_model_value
}

// Validate validates an endpoint to guarantee it won't blow up when being served
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

func strip(s string) string {
	return strings.TrimSpace(s)
}

func slice(s string) []string {
	var sl []string

	for _, p := range strings.Split(s, ",") {
		if str := strip(p); len(str) > 0 {
			sl = append(sl, strip(p))
		}
	}

	return sl
}

func New() *TRouter {
	cfg := newConfig()
	router := &TRouter{
		TGroup:     *NewGroup(),
		config:     cfg,
		middleware: newMiddlewareManager(),
		//objectPool: newPool(),
		httpCtxPool: make(map[int]*sync.Pool),
		rpcCtxPool:  make(map[int]*sync.Pool),
		exit:        make(chan bool),
	}
	cfg.Router = router
	router.respPool.New = func() interface{} {
		return &transport.THttpResponse{}
	}

	go router.watch()   // 实时订阅
	go router.refresh() // 定时刷新

	return router
}

func Default() *TRouter {
	if defaultRouter == nil {
		defaultRouter = New()
	}

	return defaultRouter
}

func (self *TRouter) PrintRoutes() {
	if self.Config().RouterTreePrinter {
		self.tree.PrintTrees()
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
func (self *TRouter) filteSelf(service *registry.Service) *registry.Service {
	curSrv := self.config.Registry.CurrentService()
	if curSrv != nil && service.Name == curSrv.Name {
		node := curSrv.Nodes[0]
		host, port, err := net.SplitHostPort(node.Address)
		if err != nil {
			log.Err(err)
		}

		nodes := make([]*registry.Node, 0)
		for _, n := range service.Nodes {
			if n.Uid == node.Uid {
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

	return service
}

// store local endpoint
func (self *TRouter) store(services []*registry.Service) {
	// services
	//names := map[string]bool{}

	// create a new endpoint mapping
	for _, service := range services {
		// set names we need later
		//names[service.Name] = true
		service = self.filteSelf(service)
		if len(service.Nodes) > 0 {
			// map per endpoint
			for _, sep := range service.Endpoints {
				//method = sep.Metadata["method"]
				//path = sep.Metadata["path"]
				r := EndpiontToRoute(sep)
				if utils.InStrings("CONNECT", sep.Method...) > 0 {
					r.handlers = append(r.handlers, generateHandler(ProxyHandler, RpcHandler, []interface{}{RpcReverseProxy}, nil, nil, []*registry.Service{service}))
				} else {
					r.handlers = append(r.handlers, generateHandler(ProxyHandler, HttpHandler, []interface{}{HttpReverseProxy}, nil, nil, []*registry.Service{service}))
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
	var attempts int

	for {
		services, err := self.config.Registry.ListServices()
		if err != nil {
			attempts++
			log.Errf("registry unable to list services: %v", err)
			time.Sleep(time.Duration(attempts) * time.Second)
			continue
		}
		// 无监视者等待
		if len(services) == 0 {
			time.Sleep(60 * time.Second)
			continue
		}

		attempts = 0

		// for each service, get service and store endpoints
		for _, s := range services {
			if self.config.Registry.CurrentService().Equal(s) {
				// 不添加自己
				continue
			}

			service, err := self.config.RegistryCacher.GetService(s.Name)
			if err != nil {
				log.Errf("unable to get service: %v", err)
				continue
			}
			self.store(service)
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

func (self *TRouter) Config() *Config {
	return self.config
}

func (self *TRouter) String() string {
	return "volts-router"
}

func (self *TRouter) Handler() interface{} {
	return self
}

func (self *TRouter) Register(ep *registry.Endpoint) error {
	if err := Validate(ep); err != nil {
		return err
	}

	//path := ep.Metadata["path"]
	return self.tree.AddRoute(EndpiontToRoute(ep))
}

func (self *TRouter) Deregister(ep *registry.Endpoint) error {
	//method := ep.Metadata["method"]
	path := ep.Metadata["path"]
	return self.tree.DelRoute(path, EndpiontToRoute(ep))
}

func (self *TRouter) Endpoints() []*registry.Endpoint {
	return self.tree.Endpoints()
}

// 注册中间件
func (self *TRouter) RegisterMiddleware(middlewares ...func() IMiddleware) {
	for _, creator := range middlewares {
		// 新建中间件
		middleware := creator()
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
func (self *TRouter) RegisterGroup(grp ...IGroup) {
	for _, g := range grp {
		self.tree.Conbine(g.GetRoutes())
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
		/*
			s.mu.Lock()
			s.activeConn[conn] = struct{}{}
			s.mu.Unlock()
		*/
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
		rsp := self.respPool.Get().(*transport.THttpResponse)
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
			ctx.route = *route
			ctx.inited = true
			ctx.Template = template.Default()
		}

		ctx.reset(rsp, r)
		ctx.setPathParams(params)

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
		//data := req.Message.Encode()
		//w.Write(data)
		//TODO 补全状态吗
		return
	}

	var (
		err error
	)

	//resMetadata := make(map[string]string)
	//newCtx := context.WithValue(context.WithValue(ctx, share.ReqMetaDataKey, req.Metadata),
	//	share.ResMetaDataKey, resMetadata)

	//		ctx := context.WithValue(context.Background(), RemoteConnContextKey, conn)
	serviceName := reqMessage.Header["ServicePath"]
	//methodName := msg.ServiceMethod
	st := reqMessage.SerializeType()
	res := transport.GetMessageFromPool()
	res.SetMessageType(transport.MT_RESPONSE)
	res.SetSerializeType(st)

	var coder codec.ICodec
	var ctx *TRpcContext
	// 获取支持的序列模式
	coder = codec.IdentifyCodec(st)
	if coder == nil {
		log.Warnf("can not find codec for %s", st.String())
		return
		//handleError(res, err)
	} else {
		route, params := self.tree.Match("CONNECT", reqMessage.Path) // 匹配路由树
		if route == nil {
			err = errors.New("rpc: can't match route " + serviceName)
			handleError(res, err)
		} else {
			// 初始化Context
			p, has := self.rpcCtxPool[route.Id]
			if !has { // TODO 优化
				p = &sync.Pool{New: func() interface{} {
					return NewRpcHandler(self)
				}}
				self.rpcCtxPool[route.Id] = p

			}

			ctx = p.Get().(*TRpcContext)
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
	defer func() {
		if self.config.Recover && self.config.RecoverHandler != nil {
			if err := recover(); err != nil {
				log.Err(err)
				self.config.RecoverHandler(ctx)
			}
		}
	}()

	// TODO:将所有需要执行的Handler 存疑列表或者树-Node保存函数和参数
	for _, h := range route.handlers {
		// TODO 回收需要特殊通道 直接调用占用了处理时间
		h.init(self).Invoke(ctx).recycle()
	}
}
