package router

// TODO 修改主控制器对应Handler名称让其可以转为统一接口完全规避反射缓慢缺陷
import (
	"errors"
	"fmt"
	"hash/crc32"
	"net/http"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"

	"github.com/volts-dev/cacher/memory"
	"github.com/volts-dev/utils"
	"github.com/volts-dev/volts/registry"
	"go.uber.org/atomic"
)

var defaultHandlerManager = newHandlerManager()

type HandlerType byte
type TransportType byte

func (self HandlerType) String() string {
	return [...]string{"LocalHandler", "ProxyHandler", "SubscribeHandler"}[self]
}

const (
	// type of handler
	LocalHandler HandlerType = iota
	ProxyHandler
	SubscriberHandler

	HttpHandler TransportType = iota
	RpcHandler
	SubscribeHandler
	ReflectHandler // TODO废弃
)

type (
	// 让控制器提供基本配置能力
	controllerInit interface {
		Init(*ControllerConfig)
	}

	controllerReady interface {
		Ready(*ControllerConfig)
	}

	// 控制器配置 可实现特殊功能
	// 功能：中间件过滤器
	ControllerConfig struct {
		mutex     sync.RWMutex
		midFilter map[string]map[string]struct{}
	}

	handle struct {
		IsFunc            bool // 是否以函数调用
		MiddlewareCreator func(IRouter) IMiddleware
		Middleware        IMiddleware               // 以供调用初始化和其他使用接口调用以达到直接调用的效率
		HttpFunc          func(*THttpContext)       // 实际调用的HTTP处理器
		RpcFunc           func(*TRpcContext)        // 实际调用的RPC处理器
		SubFunc           func(*TSubscriberContext) // 订阅处理器
	}

	// 路由节点绑定的控制器 func(Handler)
	handler struct {
		sync.RWMutex
		Config        *ControllerConfig
		_services     []*registry.Service //todo remove 该路由服务线路 提供网关等特殊服务用// Versions of this service
		service       string
		Id            int           // 根据Name生成的UID用于缓存
		Name          string        // handler名称
		FuncName      string        // handler方法名称
		Type          HandlerType   // Route 类型 决定合并的形式
		TransportType TransportType //
		pos           atomic.Int32  // current handle position

		// 多处理器包括中间件
		funcs []*handle

		// 控制器提供[结构控制器]数据和中间件载体
		ctrlName  string        // 区别不同[结构型控制器]
		ctrlType  reflect.Type  // 供生成新的控制器
		ctrlValue reflect.Value // 每个handler的控制器必须是唯一的
		ctrlModel interface{}   // 提供Ctx特殊调用
		inited    bool
	}

	handlerManager struct {
		sync.RWMutex
		handlerPool  sync.Map
		router       *TRouter
		handlerModel map[int]*handler
	}
)

func WrapHttp(fn func(IContext)) func(*THttpContext) {
	return func(ctx *THttpContext) {
		fn(ctx)
	}
}

func WrapRpc(fn func(IContext)) func(*TRpcContext) {
	return func(ctx *TRpcContext) {
		fn(ctx)
	}
}

// WrapFn is a helper function for wrapping http.HandlerFunc and returns a HttpContext.
func WrapFn(fn http.HandlerFunc) func(*THttpContext) {
	return func(ctx *THttpContext) {
		fn(ctx.response.ResponseWriter, ctx.request.Request)
	}
}

// WrapH is a helper function for wrapping http.Handler and returns a  HttpContext.
func WrapHd(h http.Handler) func(*THttpContext) {
	return func(ctx *THttpContext) {
		h.ServeHTTP(ctx.response.ResponseWriter, ctx.request.Request)
	}
}

func GetFuncName(i interface{}, seps ...rune) string {
	// 处理 nil 输入
	if i == nil {
		return ""
	}

	var runtimeFunc *runtime.Func
	if v, ok := i.(reflect.Value); ok {
		runtimeFunc = runtime.FuncForPC(v.Pointer())
	} else {
		runtimeFunc = runtime.FuncForPC(reflect.ValueOf(i).Pointer())
	}

	if runtimeFunc == nil {
		return ""
	}

	fn := runtimeFunc.Name()
	if len(seps) == 0 {
		if lastIndex := strings.LastIndexByte(fn, '.'); lastIndex != -1 {
			return fn[lastIndex+1:]
		}
		return fn
	}

	// 优化：针对常见量（1个分隔符）使用快路径
	if len(seps) == 1 {
		sep := seps[0]
		fields := strings.FieldsFunc(fn, func(r rune) bool {
			return r == sep
		})
		if size := len(fields); size > 0 {
			return fields[size-1]
		}
		return ""
	}

	// 创建分隔符查找 map
	sepMap := make(map[rune]bool, len(seps))
	for _, sep := range seps {
		sepMap[sep] = true
	}

	fields := strings.FieldsFunc(fn, func(sep rune) bool {
		return sepMap[sep]
	})

	if size := len(fields); size > 0 {
		return fields[size-1]
	}
	return ""
}

func newHandlerManager() *handlerManager {
	return &handlerManager{
		handlerModel: make(map[int]*handler),
		// handlerPool: make(map[int]*sync.Pool),
	}
}

// 生成唯一标识用于缓存
// 使用 Path、TransportType 和 HandlerType 等组合生成唯一 Id，避免同路径下的不同 TransportType (HTTP/RPC) 发生冲突
func generateHandlerId(handlerType HandlerType, tt TransportType, handlers []any, url *TUrl, service string) int {
	serviceName := "local"
	if service != "" {
		serviceName = service
	}
	idStr := fmt.Sprintf("%s:%s:%v:%v:%s", serviceName, url.Path, tt, handlerType, GetFuncName(handlers[0], filepath.Separator))
	return int(crc32.ChecksumIEEE([]byte(idStr)))
}

/*
@controller:本地服务会自行本地控制程序 其他代理远程服务为nil
*/
// 生成handler原型
// NOTE:不可用于实时环境
func generateHandler(handlerType HandlerType, tt TransportType, handlers []any, middlewares []any, url *TUrl, service string) *handler {
	uid := generateHandlerId(handlerType, tt, handlers, url, service)

	if h := defaultHandlerManager.Get(uid); h != nil {
		//h.SetServices(services) // 更新服务列表
		defaultHandlerManager.Store(h)
		return h
	}

	h := &handler{
		Id:     uid,
		Name:   fmt.Sprintf("%s-%d", GetFuncName(handlers[0], filepath.Separator), uid),
		Config: &ControllerConfig{},
		Type:   handlerType,
		//services: services,
		service: service,
		pos:     *atomic.NewInt32(-1), // 初始化位置为-1
	}
	//h.pos.Store(-1)

	// store the handler model
	defaultHandlerManager.Store(h)

	// 添加中间件的闭包
	addMiddlewares := func() {
		for _, mid := range middlewares {
			switch v := mid.(type) {
			case func(IContext):
				if tt == HttpHandler {
					h.funcs = append(h.funcs, &handle{IsFunc: true, HttpFunc: WrapHttp(v)})
				} else {
					h.funcs = append(h.funcs, &handle{IsFunc: true, RpcFunc: WrapRpc(v)})
				}
			case func(*THttpContext):
				h.funcs = append(h.funcs, &handle{IsFunc: true, HttpFunc: v})
			case func(*TRpcContext):
				h.funcs = append(h.funcs, &handle{IsFunc: true, RpcFunc: v})
			case func(IRouter) IMiddleware:
				h.funcs = append(h.funcs, &handle{IsFunc: true, MiddlewareCreator: v})
			default:
				log.Errf("unknow middleware %v", v)
			}
		}
	}

	h.TransportType = tt

	switch val := handlers[0].(type) {
	case func(*THttpContext):
		addMiddlewares()
		h.funcs = append(h.funcs, &handle{IsFunc: true, HttpFunc: val})

	case func(*TRpcContext):
		addMiddlewares()
		h.funcs = append(h.funcs, &handle{IsFunc: true, RpcFunc: val})

	case reflect.Value:
		handlerT := val.Type()
		numIn := handlerT.NumIn()

		if numIn < 1 {
			log.Panicf("the handler %s must have at least one or two arguments!", h.Name)
		}

		// 如果只有Context的情况
		if numIn == 1 {
			if handlerT.In(0) != ContextType && handlerT.In(0) != HttpContextType && handlerT.In(0) != RpcContextType {
				log.Panicf("the handler %s must include Context!", h.Name)
			}
		} else {
			// 含有接收者 (Receiver) 的情况
			recvType := handlerT.In(0)
			if recvType.Kind() != reflect.Struct && recvType.Kind() != reflect.Ptr {
				log.Panicf("the handler %s receiver must be a struct or pointer!", h.Name)
			}
			if handlerT.In(1) != ContextType && handlerT.In(1) != HttpContextType && handlerT.In(1) != RpcContextType {
				log.Panicf("the handler %s must have Context as the second argument!", h.Name)
			}
		}

		// 解析控制器结构和名称
		h.ctrlType = handlerT.In(0)
		if h.ctrlType.Kind() == reflect.Ptr {
			h.ctrlType = h.ctrlType.Elem()
		}

		if url.Controller != "" {
			h.ctrlName = url.Controller
		} else {
			h.ctrlName = h.ctrlType.Name()
		}

		h.FuncName = GetFuncName(handlers[0], '.')
		if !utils.IsStartUpper(h.FuncName) {
			log.Dbgf("the handler %s.%s must start with an upper letter!", h.ctrlName, h.FuncName)
		}

		hd, ok := h.ctrlType.MethodByName(h.FuncName)
		if !ok {
			log.Dbgf("handler %s not exist in controller %s!", h.FuncName, h.ctrlType.Name())
		}

		if tt != HttpHandler && tt != RpcHandler {
			log.Panicf("the middleware.handler %v is not supportable!", hd)
		}

		addMiddlewares()

	default:
		log.Panicf("can not generate handler by this type")
	}
	return h
}

// add which middleware name blocked handler name
func (self *ControllerConfig) AddFilter(middleware string, handlers ...string) {
	self.mutex.Lock()
	defer self.mutex.Unlock()

	if self.midFilter == nil {
		self.midFilter = make(map[string]map[string]struct{})
	}

	lst, ok := self.midFilter[middleware]
	if !ok {
		lst = make(map[string]struct{})
		self.midFilter[middleware] = lst
	}

	for _, name := range handlers {
		// Use lowercase for case-insensitive matching
		lst[strings.ToLower(name)] = struct{}{}
	}
}

func (self *handlerManager) Store(h *handler) {
	self.Lock()
	defer self.Unlock()
	log.Dbgf("%d,%s", h.Id, h.Type.String())
	self.handlerModel[h.Id] = h
}

func (self *handlerManager) Get(id int) *handler {
	if v, ok := self.handlerPool.Load(id); ok {
		if h := v.(memory.StackCache).Pop(); h != nil {
			return h.(*handler)
		}
	}

	self.RLock()
	defer self.RUnlock()
	if hd, ok := self.handlerModel[id]; ok {
		return hd.clone()
	}
	return nil
}

func (self *handlerManager) Put(id int, h *handler) {
	var c memory.StackCache

	if v, ok := self.handlerPool.Load(id); ok {
		c = v.(memory.StackCache)
	} else {
		c = memory.NewStack(
			memory.WithInterval(60),
			memory.WithExpire(3600),
		) // cacher
		self.handlerPool.Store(id, c)
	}

	c.Push(h)
}

func (self *handler) Services() []*registry.Service {
	self.RLock()
	defer self.RUnlock()
	return self._services
}

func (self *handler) Service() string {
	return self.service
}

func (self *handler) SetServices(services []*registry.Service) {
	self.Lock()
	defer self.Unlock()
	self._services = services
}

// 处理器名称
func (self *handler) String() string {
	return self.FuncName
}

// 控制器名称如果无控制器为空
func (self *handler) ControllerName() string {
	return self.ctrlName
}

// 控制器实例
func (self *handler) Controller() any {
	return self.ctrlModel
}

// 初始化生成新的处理器
// 任务:创建/缓存/初始化
// 调用Recycle回收
func (self *handler) init(router *TRouter) {
	self.Lock()
	defer self.Unlock()

	if self.inited {
		return
	}

	if self.ctrlName != "" {
		// 新建控制器
		self.ctrlValue = reflect.New(self.ctrlType)
		ctrl := self.ctrlValue.Elem()

		/* 代码位置不可移动 */
		// 初始化控制器配置
		// 断言必须非Elem()
		if c, ok := self.ctrlValue.Interface().(controllerInit); ok {
			c.Init(self.Config)
		}

		var (
			midVal  reflect.Value
			midTyp  reflect.Type
			midName string // name of middleware
		)

		// 赋值中间件
		// 修改成员
		for i := 0; i < ctrl.NumField(); i++ {
			midVal = ctrl.Field(i) // get the middleware value
			if !midVal.CanSet() {
				log.Warnf("Field %s in controller %s is not exported and cannot be set.", ctrl.Type().Field(i).Name, self.ctrlName)
				continue
			}

			midTyp = midVal.Type() // get the middleware type

			if midTyp.Kind() == reflect.Ptr {
				midTyp = midTyp.Elem()
			}

			// get the name of middleware from the Type or Name()
			midName = midTyp.String()
			var midItf any
			if midVal.Kind() == reflect.Ptr {
				// 使用命名中间件
				midItf = midVal.Interface()
			} else if midVal.Kind() == reflect.Struct && ctrl.Type().Field(0).Name == midTyp.Name() {
				// 非指针且是继承的检测
				// 由于 继承的变量名=结构名称
				// Example:Event
				midItf = ctrl.Interface()
			}

			// 过滤非中间件
			if _, ok := midItf.(IMiddleware); !ok {
				continue
			}

			// 自定义名称
			if m, ok := midItf.(IMiddlewareName); ok { // ctrl must be a value pointer to provide pointer methods
				midName = m.Name()
			}

			ml := router.middleware.Get(midName)
			if ml == nil {
				log.Panicf("Controller %s need middleware %s to be registered!", self.ctrlName, midName)
			}

			midNewVal := reflect.ValueOf(ml(router))
			/***	类型一致性检查!过滤指针中间件!
				type Controller struct {
					Session *TSession
				}
			***/
			if midVal.Kind() == reflect.Ptr {
				if !midNewVal.Type().AssignableTo(midVal.Type()) {
					log.Errf("Middleware %s type mismatch for field %s", midName, ctrl.Type().Field(i).Name)
					continue
				}
				midVal.Set(midNewVal) // WARM: Field must exportable
			} else if midVal.Kind() == reflect.Struct {
				if !midNewVal.Elem().Type().AssignableTo(midVal.Type()) {
					log.Errf("Middleware %s type mismatch for field %s", midName, ctrl.Type().Field(i).Name)
					continue
				}
				midVal.Set(midNewVal.Elem())
			}

			// 检测中间件黑名单
			self.Config.mutex.RLock()
			filters, ok := self.Config.midFilter[midName]
			if ok {
				_, skip := filters[strings.ToLower(self.FuncName)]
				self.Config.mutex.RUnlock()
				if skip {
					continue
				}
			} else {
				self.Config.mutex.RUnlock()
			}

			// 添加中间件
			if midVal.Kind() == reflect.Struct && ctrl.Type().Field(0).Name == midVal.Type().Name() {
				// 非指针且是继承的检测
				// 由于 继承的变量名=结构名称
				// Example:Event
				//log.Dbg(ctrl.Interface(), midVal.Type().String(), ctrl.Interface().(IMiddleware))
				if mw, ok := ctrl.Interface().(IMiddleware); ok {
					self.funcs = append(self.funcs, &handle{IsFunc: false, Middleware: mw})
				}
			} else {
				//log.Dbg(midVal.Interface(), midVal.Type().String(), midVal.Interface().(IMiddleware))
				if mw, ok := midVal.Interface().(IMiddleware); ok {
					self.funcs = append(self.funcs, &handle{IsFunc: false, Middleware: mw})
				}
			}
		}

		// 写入handler
		// MethodByName特性取到的值receiver默认为v
		hd := ctrl.MethodByName(self.FuncName)
		if !hd.IsValid() {
			log.Errf("Method %s not found in controller %s", self.FuncName, self.ctrlName)
			//goto initMiddlewares
		}

		switch hd.Type().In(0) {
		case ContextType:
			if fn, ok := hd.Interface().(func(IContext)); ok {
				if self.TransportType != HttpHandler {
					self.funcs = append(self.funcs, &handle{IsFunc: true, RpcFunc: WrapRpc(fn)})
				} else {
					self.funcs = append(self.funcs, &handle{IsFunc: true, HttpFunc: WrapHttp(fn)})
				}
			}
		case RpcContextType:
			self.funcs = append(self.funcs, &handle{IsFunc: true, RpcFunc: hd.Interface().(func(*TRpcContext))})
		case HttpContextType:
			self.funcs = append(self.funcs, &handle{IsFunc: true, HttpFunc: hd.Interface().(func(*THttpContext))})
		default:
			log.Errf("the handler %v is not supportable!", hd)
		}

		// 所有的指针准备就绪后生成Interface
		// NOTE:必须初始化结构指针才能生成完整的Model,之后修改CtrlValue将不会有效果
		self.ctrlModel = self.ctrlValue.Interface()

		// 初始化控制器配置
		// 断言必须非Elem()
		if c, ok := self.ctrlModel.(controllerReady); ok {
			c.Ready(self.Config)
		}
	}

	/* 初始化中间件 */
	for _, handle := range self.funcs {
		if handle.Middleware == nil && handle.MiddlewareCreator != nil {
			middleware := handle.MiddlewareCreator(router)
			handle.Middleware = middleware
			handle.HttpFunc = WrapHttp(middleware.Handler)
			handle.RpcFunc = WrapRpc(middleware.Handler)
		}
	}

	self.reset()
	self.inited = true
}

func (self *handler) clone() *handler {
	self.RLock()
	defer self.RUnlock()

	h := &handler{
		Config:        self.Config,
		Id:            self.Id,
		Name:          self.Name,
		FuncName:      self.FuncName,
		Type:          self.Type,
		TransportType: self.TransportType,
		funcs:         nil, // 初始化为空，克隆时深度拷贝
		ctrlName:      self.ctrlName,
		ctrlType:      self.ctrlType,
		ctrlValue:     self.ctrlValue,
		ctrlModel:     self.ctrlModel,
		service:       self.service,
	}

	if self.funcs != nil {
		h.funcs = make([]*handle, len(self.funcs))
		copy(h.funcs, self.funcs)
	}

	h.pos.Store(self.pos.Load())

	/*
		if self.services != nil {
			h.services = make([]*registry.Service, len(self.services))
			copy(h.services, self.services)
		}*/

	return h
}

// 统一处理 HTTP 和 RPC 的 handler 调用逻辑
func (self *handler) invokeHandlers(ctx IContext, tansType TransportType) error {
	for {
		pos := int(self.pos.Load())
		if pos >= len(self.funcs) {
			break
		}
		if ctx.IsDone() {
			break
		}

		hd := self.funcs[pos]
		if hd.IsFunc {
			switch self.TransportType {
			case HttpHandler:
				c, ok := ctx.(*THttpContext)
				if !ok {
					return errors.New("invalid context type for HttpHandler")
				}
				hd.HttpFunc(c)
			case RpcHandler:
				c, ok := ctx.(*TRpcContext)
				if !ok {
					return errors.New("invalid context type for RpcHandler")
				}
				hd.RpcFunc(c)
			case ReflectHandler:
				return errors.New("ReflectHandler not implemented") // 修复格式化参数缺失
				/*
					for self.pos < len(self.funcs) {
						if ctx.IsDone() {
							break
						}
						v := self.funcs[self.pos]
						rcv := self.FuncsReceiver[self.pos]
						if rcv.IsValid() {
							v.Call([]reflect.Value{rcv, ctx.ValueModel()})
						} else {
							v.Call([]reflect.Value{ctx.ValueModel()})
						}

						self.pos++
					}
				*/
			}
		} else {
			hd.Middleware.Handler(ctx)
		}

		self.pos.Inc()
	}

	return nil
}

func (self *handler) Invoke(ctx IContext) {
	ctx.setHandler(self)
	self.pos.Inc()

	if err := self.invokeHandlers(ctx, self.TransportType); err != nil {
		log.Panic(err)
	}
}

// InvokeSubscriber 专用于订阅消息分发，不经过 IContext 路径
func (self *handler) InvokeSubscriber(ctx *TSubscriberContext) error {
	for _, hd := range self.funcs {
		if hd.SubFunc != nil {
			hd.SubFunc(ctx)
		}
	}
	return nil
}

// 初始化
// NOTE:仅限于回收使用
func (self *handler) reset() *handler {
	// 执行中间件初始化
	for _, handle := range self.funcs {
		if handle.Middleware != nil {
			if m, ok := handle.Middleware.(IMiddlewareReset); ok {
				m.Reset()
			}
		}
	}

	self.pos.Store(-1) // 重置位置
	return self
}
