package router

// TODO 修改主控制器对应Handler名称让其可以转为统一接口完全规避反射缓慢缺陷
import (
	"hash/crc32"
	"net/http"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"

	"github.com/volts-dev/utils"
	"github.com/volts-dev/volts/registry"
)

var deflautHandlerManager = newHandlerManager()

type HandlerType byte
type TransportType byte

func (self HandlerType) String() string {
	return [...]string{"LocalHandler", "ProxyHandler"}[self]
}

const (
	// type of handler
	LocalHandler HandlerType = iota
	ProxyHandler

	HttpHandler TransportType = iota
	RpcHandler
	ReflectHandler
)

type (
	// 让控制器提供基本配置能力
	controllerInit interface {
		Init(*ControllerConfig)
	}

	handlerManager struct {
		handlerPool map[int]*sync.Pool
		router      *TRouter
	}

	// 控制器配置 可实现特殊功能
	// 功能：中间件过滤器
	ControllerConfig struct {
		midFilter map[string][]string // TODO sync
	}

	handle struct {
		IsFunc     bool                // 是否以函数调用
		Middleware IMiddleware         // 以供调用初始化和其他使用接口调用以达到直接调用的效率
		HttpFunc   func(*THttpContext) // 实际调用的HTTP处理器
		RpcFunc    func(*TRpcContext)  // 实际调用的RPC处理器
	}

	// 路由节点绑定的控制器 func(Handler)
	handler struct {
		Manager  *handlerManager
		Config   *ControllerConfig
		Services []*registry.Service // 该路由服务线路 提供网关等特殊服务用// Versions of this service

		Id            int           // 根据Name生成的UID用于缓存
		Name          string        // handler名称
		FuncName      string        // handler方法名称
		Type          HandlerType   // Route 类型 决定合并的形式
		TransportType TransportType //
		pos           int           // current handle position

		// 多处理器包括中间件
		funcs []*handle

		// 控制器提供[结构控制器]数据和中间件载体
		ctrlType  reflect.Type  // 供生成新的控制器
		ctrlName  string        // 区别不同[结构型控制器]
		ctrlValue reflect.Value // 每个handler的控制器必须是唯一的
		ctrlModel interface{}   // 提供Ctx特殊调用
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

// add which middleware name blocked handler name
func (self *ControllerConfig) AddFilter(middleware string, handlers ...string) {
	if self.midFilter == nil {
		self.midFilter = make(map[string][]string)
	}
	lst := self.midFilter[middleware]
	for _, name := range handlers {
		if utils.InStrings(name, lst...) == -1 {
			lst = append(lst, name)
		}
	}
	self.midFilter[middleware] = lst
}

func GetFuncName(i interface{}, seps ...rune) string {
	// 获取函数名称
	var fn string
	if v, ok := i.(reflect.Value); ok {
		fn = v.Type().String()
		fn = runtime.FuncForPC(v.Pointer()).Name()
	} else {
		fn = runtime.FuncForPC(reflect.ValueOf(i).Pointer()).Name()
	}

	// 用 seps 进行分割
	fields := strings.FieldsFunc(fn, func(sep rune) bool {
		for _, s := range seps {
			if sep == s {
				return true
			}
		}
		return false
	})

	if size := len(fields); size > 0 {
		return fields[size-1]
	}
	return ""
}

func newHandlerManager() *handlerManager {
	return &handlerManager{
		handlerPool: make(map[int]*sync.Pool),
	}
}

/*
@controller:本地服务会自行本地控制程序 其他代理远程服务为nil
*/
// 生成handler原型
// NOTE:不可用于实时环境
func generateHandler(hanadlerType HandlerType, tt TransportType, handlers []any, middlewares []any, url *TUrl, services []*registry.Service) *handler {
	httpFn := func(h *handler, middlewares []any) {
		for _, mid := range middlewares {
			switch v := mid.(type) {
			case func(IContext):
				h.funcs = append(h.funcs, &handle{IsFunc: true, HttpFunc: WrapHttp(v)})
			case func(*THttpContext):
				h.funcs = append(h.funcs, &handle{IsFunc: true, HttpFunc: v})
			case func() IMiddleware: // 创建新的中间件状态实例并调用传递中间件
				middleware := v()
				h.funcs = append(h.funcs, &handle{IsFunc: true, Middleware: middleware, HttpFunc: WrapHttp(middleware.Handler)})
			default:
				log.Errf("unknow middleware %v", v)
			}
		}
	}
	rpcFn := func(h *handler, middlewares []any) {
		for _, mid := range middlewares {
			switch v := mid.(type) {
			case func(IContext):
				h.funcs = append(h.funcs, &handle{IsFunc: true, RpcFunc: WrapRpc(v)})
			case func(*TRpcContext):
				h.funcs = append(h.funcs, &handle{IsFunc: true, RpcFunc: v})
			case func() IMiddleware:
				middleware := v()
				h.funcs = append(h.funcs, &handle{IsFunc: true, Middleware: middleware, RpcFunc: WrapRpc(middleware.Handler)})
			default:
				log.Errf("unknow middleware %v", v)
			}
		}
	}
	h := &handler{
		Manager:  deflautHandlerManager,
		Config:   &ControllerConfig{},
		Type:     hanadlerType,
		Services: services,
		pos:      -1,
	}
	// 生成唯一标识用于缓存
	h.Name = GetFuncName(handlers[0], filepath.Separator)
	h.Id = int(crc32.ChecksumIEEE([]byte(h.Name)))

	switch val := handlers[0].(type) {
	case func(*THttpContext):
		h.TransportType = HttpHandler
		httpFn(h, middlewares)
		h.funcs = append(h.funcs, &handle{IsFunc: true, HttpFunc: val})
	case func(*TRpcContext):
		h.TransportType = RpcHandler
		rpcFn(h, middlewares)
		h.funcs = append(h.funcs, &handle{IsFunc: true, RpcFunc: val})
	case reflect.Value:
		// 以下处理带控制器的处理器
		// Example: ctroller.method
		handlerType := val.Type()
		numIn := handlerType.NumIn()
		if numIn == 1 && (handlerType.In(0) != ContextType && handlerType.In(0) != HttpContextType && handlerType.In(0) != RpcContextType) {
			log.Panicf("the handler %s must including Context!", h.Name)
		}
		if handlerType.In(0).Kind() != reflect.Struct || (handlerType.In(1) != ContextType && handlerType.In(1) != HttpContextType && handlerType.In(1) != RpcContextType) {
			log.Panicf("the handler %s receiver must not be a pointer and with HTTP/RPC context!", h.Name)
		}
		// 这是控制器上的某个方法 提取控制器和中间件
		h.ctrlType = handlerType.In(0)
		if h.ctrlType.Kind() == reflect.Pointer {
			h.ctrlType = h.ctrlType.Elem()
		}

		// 变更控制器名称
		if url.Controller != "" {
			h.ctrlName = url.Controller
		} else {
			h.ctrlName = h.ctrlType.Name()
		}

		// 检测获取绑定的handler
		h.FuncName = GetFuncName(handlers[0], '.')
		if !utils.IsStartUpper(h.FuncName) {
			log.Dbgf("the handler %s.%s must start with upper letter!", h.ctrlName, h.FuncName)
		}

		hd, ok := h.ctrlType.MethodByName(h.FuncName)
		if !ok {
			log.Dbgf("handler %s not exist in controller %s!", h.FuncName, h.ctrlType.Name())
		}

		h.TransportType = tt
		switch tt {
		case HttpHandler:
			httpFn(h, middlewares)
		case RpcHandler:
			rpcFn(h, middlewares)
		default:
			log.Panicf("the middleware.handler %v is not supportable!", hd)
		}
		/*
			var midVal reflect.Value
			//var midhd reflect.Value
			for i := 0; i < h.ctrlValue.NumField(); i++ {
				midVal = h.ctrlValue.Field(i) // get the middleware value

				if midVal.Kind() == reflect.Struct && h.ctrlValue.Type().Field(0).Name == midVal.Type().Name() {
					// 非指针且是继承的检测
					// 由于 继承的变量名=结构名称
					// Example:Event
					//midhd = ctrl.MethodByName("Handler")
					log.Dbg(ctrl.Interface(), midVal.Type().String(), ctrl.Interface().(IMiddleware))
					if mw, ok := ctrl.Interface().(IMiddleware); ok {
						if h.TransportType != HttpHandler {
							h.RpcFuncs = append(h.RpcFuncs, &handle{IsFunc: false, Middleware: mw})
						} else {
							h.HttpFuncs = append(h.HttpFuncs, &handle{IsFunc: false, Middleware: mw})
						}
					}
				} else {
					//log.Dbg(midVal.Interface(), midVal.Type().String(), midVal.Interface().(IMiddleware))
					if mw, ok := midVal.Interface().(IMiddleware); ok {
						if h.TransportType != HttpHandler {
							h.RpcFuncs = append(h.RpcFuncs, &handle{IsFunc: false, Middleware: mw})
						} else {
							h.HttpFuncs = append(h.HttpFuncs, &handle{IsFunc: false, Middleware: mw})
						}
					}
					//midhd = midVal.MethodByName("Handler")
				}
				/*
					switch midhd.Type().In(0) {
					case HttpContextType:
						if h.TransportType != HttpHandler {
							log.Panicf("handler and its middleware must one of Http or RPC context!")
						}
						h.HttpFuncs = append(h.HttpFuncs, &handle{IsFunc: false, HttpFunc: midhd.Interface().(func(*THttpContext))})
					case RpcContextType:
						if h.TransportType != RpcHandler {
							log.Panicf("handler and its middleware must one of Http or RPC context!")
						}
						h.RpcFuncs = append(h.RpcFuncs, &handle{IsFunc: false, RpcFunc: midhd.Interface().(func(*TRpcContext))})
					case ContextType:
						if h.TransportType == HttpHandler {
							h.HttpFuncs = append(h.HttpFuncs, func(ctx *THttpContext) {
								fn := midhd.Interface().(func(IContext))
								fn(ctx)
							})
							break
						} else if h.TransportType == RpcHandler {
							h.RpcFuncs = append(h.RpcFuncs, func(ctx *TRpcContext) {
								fn := midhd.Interface().(func(IContext))
								fn(ctx)
							})
							break
						}
						fallthrough
					default:
						log.Panicf("the middleware.handler %v is not supportable!", midhd.String())
					}
		*/
	/*	}


	 */
	default:
		log.Panicf("can not gen handler by this type")
	}
	return h
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
func (self *handler) init(router *TRouter) *handler {
	p, has := self.Manager.handlerPool[self.Id]
	if !has {
		p = &sync.Pool{}
		self.Manager.handlerPool[self.Id] = p
	}

	itf := p.Get()
	if itf != nil {
		return itf.((*handler))
	}

	h := &handler{}
	*h = *self //复制handler原型
	if h.ctrlName != "" {
		// 新建控制器
		ctrl := reflect.New(h.ctrlType)
		h.ctrlValue = ctrl.Elem()

		// 初始化控制器配置
		if c, ok := ctrl.Interface().(controllerInit); ok {
			c.Init(h.Config)
		}

		var (
			midVal  reflect.Value
			midTyp  reflect.Type
			midName string // name of middleware
		)

		// 赋值中间件
		// 修改成员
		for i := 0; i < h.ctrlValue.NumField(); i++ {
			midVal = h.ctrlValue.Field(i) // get the middleware value
			midTyp = midVal.Type()        // get the middleware type

			if midTyp.Kind() == reflect.Ptr {
				midTyp = midTyp.Elem()
			}

			// get the name of middleware from the Type or Name()
			midName = midTyp.String()
			var midItf any
			if midVal.Kind() == reflect.Ptr {
				// 使用命名中间件
				midItf = midVal.Interface()
			} else if midVal.Kind() == reflect.Struct && h.ctrlValue.Type().Field(0).Name == midTyp.Name() {
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
				log.Panicf("Controller %s need middleware %s to be registered!", h.ctrlName, midName)
			}

			midNewVal := reflect.ValueOf(ml())
			/***	!过滤指针中间件!
				type Controller struct {
					Session *TSession
				}
			***/
			if midVal.Kind() == reflect.Ptr {
				midVal.Set(midNewVal) // WARM: Field must exportable
			} else if midVal.Kind() == reflect.Struct {
				midVal.Set(midNewVal.Elem())
			}

			// 检测中间件黑名单
			if lst, ok := h.Config.midFilter[midName]; ok {
				for _, name := range lst {
					if strings.ToLower(name) == strings.ToLower(h.FuncName) {
						goto next
					}
				}
			}

			// 添加中间件
			if midVal.Kind() == reflect.Struct && h.ctrlValue.Type().Field(0).Name == midVal.Type().Name() {
				// 非指针且是继承的检测
				// 由于 继承的变量名=结构名称
				// Example:Event
				//log.Dbg(ctrl.Interface(), midVal.Type().String(), ctrl.Interface().(IMiddleware))
				if mw, ok := ctrl.Interface().(IMiddleware); ok {
					h.funcs = append(h.funcs, &handle{IsFunc: false, Middleware: mw})
				}
			} else {
				//log.Dbg(midVal.Interface(), midVal.Type().String(), midVal.Interface().(IMiddleware))
				if mw, ok := midVal.Interface().(IMiddleware); ok {
					h.funcs = append(h.funcs, &handle{IsFunc: false, Middleware: mw})
				}
			}
		next:
		}

		// 写入handler
		// MethodByName特性取到的值receiver默认为v
		hd := h.ctrlValue.MethodByName(h.FuncName)
		switch hd.Type().In(0) {
		case ContextType:
			if fn, ok := hd.Interface().(func(IContext)); ok {
				if h.TransportType != HttpHandler {
					h.funcs = append(h.funcs, &handle{IsFunc: true, RpcFunc: WrapRpc(fn)})
				} else {
					h.funcs = append(h.funcs, &handle{IsFunc: true, HttpFunc: WrapHttp(fn)})
				}
			}
		case RpcContextType:
			h.funcs = append(h.funcs, &handle{IsFunc: true, RpcFunc: hd.Interface().(func(*TRpcContext))})
		case HttpContextType:
			h.funcs = append(h.funcs, &handle{IsFunc: true, HttpFunc: hd.Interface().(func(*THttpContext))})
		default:
			log.Panicf("the handler %v is not supportable!", hd)
		}

		// 所有的指针准备就绪后生成Interface
		// NOTE:必须初始化结构指针才能生成完整的Model,之后修改CtrlValue将不会有效果
		h.ctrlModel = h.ctrlValue.Interface()
	}

	h.reset()
	return h
}

func (self *handler) Invoke(ctx IContext) *handler {
	ctx.setHandler(self)

	self.pos++

	// 废弃替代 set the controller to context vars
	//if self.ctrlModel != nil {
	//	ctx.Data().FieldByName("controller").AsInterface(self.ctrlModel)
	//	ctx.Data().FieldByName("controller_name").AsString(self.ctrlName)
	//}

	//
	switch self.TransportType {
	case HttpHandler:
		c := ctx.(*THttpContext)
		for self.pos < len(self.funcs) {
			if c.IsDone() {
				break
			}
			hd := self.funcs[self.pos]
			if hd.IsFunc {
				hd.HttpFunc(c)
			} else {
				hd.Middleware.Handler(c)
			}
			self.pos++
		}
	case RpcHandler:
		c := ctx.(*TRpcContext)
		for self.pos < len(self.funcs) {
			if c.IsDone() {
				break
			}
			hd := self.funcs[self.pos]
			if hd.IsFunc {
				hd.RpcFunc(c)
			} else {
				hd.Middleware.Handler(c)
			}
			self.pos++
		}
	case ReflectHandler:
		log.Panicf("ReflectHandler %v")
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

	return self
}

// 初始化
// NOTE:仅限于回收使用
func (self *handler) reset() *handler {
	// 执行中间件初始化
	for _, handle := range self.funcs {
		if handle.Middleware != nil {
			if m, ok := handle.Middleware.(IMiddlewareRest); ok {
				m.Rest()
			}
		}
	}

	self.pos = -1
	return self
}

func (self *handler) recycle() *handler {
	p := self.Manager.handlerPool[self.Id]
	self.reset()
	p.Put(self)

	return self
}
