// Package router handles message serving and routing logic for both HTTP and RPC protocols.
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
		// exit channel signals background tasks to stop.
		exit chan bool
	}
)

// Validate checks if an endpoint is valid for being served.
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
	} else {
		// Serve as a standard web/HTTP request.
		var rsp *transport.THttpResponse
		if v := self.respPool.Get(); v == nil {
			rsp = transport.NewHttpResponse(r.Context(), r)
		} else {
			rsp = v.(*transport.THttpResponse)
		}
		rsp.Connect(w)

		// Match route from the internal tree.
		route, params := self.tree.Match(r.Method, r.URL.Path)
		if route == nil {
			rsp.WriteHeader(http.StatusNotFound)
			return
		}

		// Retrieve or create a context pool for this route.
		p, _ := self.httpCtxPool.LoadOrStore(route.Id(), &sync.Pool{New: func() interface{} {
			return NewHttpContext(self)
		}})

		pool := p.(*sync.Pool)
		ctx := pool.Get().(*THttpContext)
		ctx.route = route
		if !ctx.inited {
			ctx.router = self
			ctx.inited = true
			ctx.Template = template.Default()
		}
		ctx.reset(rsp, r)
		ctx.setPathParams(params)

		// Execute the routing chain.
		self.route(route, ctx)

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
	coder := codec.IdentifyCodec(st)
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

		pool := p.(*sync.Pool)
		ctx := pool.Get().(*TRpcContext)
		if !ctx.inited {
			ctx.router = self
			ctx.route = route
			ctx.inited = true
		}
		ctx.reset(w, r, self, route)
		ctx.setPathParams(params)

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

// filteSelf filters out the nodes that belong to the current instance from a service.
func (self *TRouter) filteSelf(srv *registry.Service, localSet map[string]struct{}) *registry.Service {
	service := *srv // Shallow copy to avoid mutating the cached original service object

	// If localSet is nil, it indicates a safety shield condition.
	if localSet == nil {
		service.Nodes = nil
		return &service
	}

	nodes := make([]*registry.Node, 0, len(service.Nodes))
	for _, n := range service.Nodes {
		// Check if the node ID matches any local node ID to prevent infinite request loops during forwarding.
		if _, hasId := localSet[n.Id]; hasId {
			continue
		}

		nodes = append(nodes, n)
	}

	service.Nodes = nodes
	return &service
}

// store maps registry services to internal endpoints.
func (self *TRouter) store(services []*registry.Service) {
	if len(services) == 0 {
		return
	}

	// Pre-calculate the local node set to avoid O(N*M) lookups in the loop.
	var localSet map[string]struct{}
	localServices := self.config.RegistryCacher.LocalServices()

	// Handle the case where the monitoring fetch happens before local services are fully registered.
	if len(localServices) == 0 && self.tree.Count.Load() > 0 {
		localSet = nil // Enter temporary empty node state.
	} else {
		localSet = make(map[string]struct{})
		for _, curSrv := range localServices {
			if curSrv == nil {
				continue
			}
			for _, node := range curSrv.Nodes {
				localSet[node.Id] = struct{}{}
			}
		}
	}

	// Create a new endpoint mapping.
	for _, service := range services {
		service = self.filteSelf(service, localSet)
		if len(service.Nodes) > 0 {
			var handlerId int
			for _, sep := range service.Endpoints {
				url := &TUrl{
					Path: sep.Path,
				}
				r := EndpiontToRoute(sep)

				// Determine the handler type (HTTP or RPC/CONNECT).
				isConnect := utils.IndexOf("CONNECT", sep.Method...) != -1
				if isConnect {
					handlerId = generateHandler(ProxyHandler, RpcHandler, []interface{}{RpcReverseProxy}, nil, url, service.Name).Id

				} else {
					handlerId = generateHandler(ProxyHandler, HttpHandler, []interface{}{HttpReverseProxy}, nil, url, service.Name).Id
				}

				r.handlers = append(r.handlers, handlerId)
				if err := self.tree.AddRoute(r); err != nil {
					log.Err(err)
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
				log.Errf("unable to get service: %v", err)
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

	localServices := self.config.RegistryCacher.LocalServices()

	for {
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
					log.Errf("unable to get service: %v", err)
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
