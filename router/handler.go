package router

// TODO 修改主控制器对应Handler名称让其可以转为统一接口完全规避反射缓慢缺陷
import (
	"hash/crc32"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"

	"github.com/volts-dev/volts/registry"
)

var deflautHandlerManager = newHandlerManager()

type HandlerType byte
type TransportType byte

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
		midFilter map[string][]string
	}

	handle struct {
		IsFunc     bool
		Middleware IMiddleware // 使用接口调用以达到直接调用的效率
		HttpFunc   func(*THttpContext)
		RpcFunc    func(*TRpcContext)
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
		Pos           int           // current handle position

		// 处理器
		Funcs []*handle

		// 控制器提供[结构控制器]数据和中间件载体
		CtrlType  reflect.Type  // 供生成新的控制器
		CtrlName  string        // 区别不同[结构型控制器]
		CtrlValue reflect.Value // 每个handler的控制器必须是唯一的
		CtrlModel interface{}   // 提供Ctx特殊调用
	}
)

func (self HandlerType) String() string {
	return [...]string{"LocalHandler", "ProxyHandler"}[self]
}

// add which middleware name blocked handler name
func (self *ControllerConfig) AddFilter(middleware string, handlers ...string) {
	if self.midFilter == nil {
		self.midFilter = make(map[string][]string)
	}
	self.midFilter[middleware] = handlers
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
func generateHandler(hanadlerType HandlerType, handlers []interface{}, services []*registry.Service) *handler {
	h := &handler{
		Manager:  deflautHandlerManager,
		Config:   &ControllerConfig{},
		Type:     hanadlerType,
		Services: services,
		Pos:      -1,
	}
	// 生成唯一标识用于缓存
	h.Name = GetFuncName(handlers[0], filepath.Separator)
	h.Id = int(crc32.ChecksumIEEE([]byte(h.Name)))

	switch val := handlers[0].(type) {
	case func(*THttpContext):
		h.TransportType = HttpHandler
		for i, hd := range handlers {
			if i == 0 {
				continue // 0 即是Val将插在最后
			}
			if v, ok := hd.(func(*THttpContext)); ok {
				//h.HttpFuncs = append(h.HttpFuncs, v)
				h.Funcs = append(h.Funcs, &handle{IsFunc: true, HttpFunc: v})

			}
		}
		h.Funcs = append(h.Funcs, &handle{IsFunc: true, HttpFunc: val})
	case func(*TRpcContext):
		h.TransportType = RpcHandler
		for i, hd := range handlers {
			if i == 0 {
				continue
			}
			if v, ok := hd.(func(*TRpcContext)); ok {
				h.Funcs = append(h.Funcs, &handle{IsFunc: true, RpcFunc: v})
			}
		}
		h.Funcs = append(h.Funcs, &handle{IsFunc: true, RpcFunc: val})
	case reflect.Value:
		// 以下处理带控制器的处理器
		// Example: ctroller.method
		handlerType := val.Type()
		numIn := handlerType.NumIn()
		if numIn == 1 && (handlerType.In(0) != HttpContextType && handlerType.In(0) != RpcContextType) {
			log.Panicf("the handler %s must including Context!", h.Name)
		}
		if handlerType.In(0).Kind() != reflect.Struct || (handlerType.In(1) != HttpContextType && handlerType.In(1) != RpcContextType) {
			log.Panicf("the handler %s receiver must not be a pointer and with HTTP/RPC context!", h.Name)
		}
		// 这是控制器上的某个方法 提取控制器和中间件
		h.CtrlType = handlerType.In(0)
		if h.CtrlType.Kind() == reflect.Pointer {
			h.CtrlType = h.CtrlType.Elem()
		}
		h.CtrlName = h.CtrlType.Name()

		// 检测获取绑定的handler
		h.FuncName = GetFuncName(handlers[0], '.')
		hd, _ := h.CtrlType.MethodByName(h.FuncName)
		switch hd.Type.In(1) {
		case HttpContextType:
			h.TransportType = HttpHandler
		case RpcContextType:
			h.TransportType = RpcHandler
		default:
			log.Panicf("the middleware.handler %v is not supportable!", hd)
		}
		/*
			var midVal reflect.Value
			//var midhd reflect.Value
			for i := 0; i < h.CtrlValue.NumField(); i++ {
				midVal = h.CtrlValue.Field(i) // get the middleware value

				if midVal.Kind() == reflect.Struct && h.CtrlValue.Type().Field(0).Name == midVal.Type().Name() {
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

		// 写入handler
		if h.TransportType != HttpHandler {
			h.RpcFuncs = append(h.RpcFuncs, &handle{IsFunc: true, RpcFunc: hd.Interface().(func(*TRpcContext))})
		} else {
			h.HttpFuncs = append(h.HttpFuncs, &handle{IsFunc: true, HttpFunc: hd.Interface().(func(*THttpContext))})
		}
	*/
	default:
		log.Panicf("can not gen handler by this type")
	}
	return h
}

// 缓存或者创建新的
func (self *handler) New(router *TRouter) *handler {
	p := self.Manager.handlerPool[self.Id]
	itf := p.Get()
	if itf != nil {
		return itf.((*handler))
	}

	h := &handler{}
	*h = *self //复制handler原型
	if h.CtrlName != "" {
		// 新建控制器
		ctrl := reflect.New(h.CtrlType)
		h.CtrlValue = ctrl.Elem()

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
		for i := 0; i < h.CtrlValue.NumField(); i++ {
			midVal = h.CtrlValue.Field(i) // get the middleware value
			midTyp = midVal.Type()        // get the middleware type

			if midTyp.Kind() == reflect.Ptr {
				midTyp = midTyp.Elem()
			}

			// get the name of middleware from the Type or Name()
			midName = midTyp.String()
			if midVal.Kind() == reflect.Ptr {
				if m, ok := midVal.Interface().(IMiddlewareName); ok {
					midName = m.Name()
				}
			} else if midVal.Kind() == reflect.Struct && h.CtrlValue.Type().Field(0).Name == midTyp.Name() {
				// 非指针且是继承的检测
				// 由于 继承的变量名=结构名称
				// Example:Event
				if m, ok := ctrl.Interface().(IMiddlewareName); ok { // ctrl must be a value pointer to provide pointer methods
					midName = m.Name()
				}
			}

			ml := router.middleware.Get(midName)
			if ml == nil {
				log.Panicf("Controller %s need middleware %s to be registered!", h.CtrlName, midName)
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
			if midVal.Kind() == reflect.Struct && h.CtrlValue.Type().Field(0).Name == midVal.Type().Name() {
				// 非指针且是继承的检测
				// 由于 继承的变量名=结构名称
				// Example:Event
				//log.Dbg(ctrl.Interface(), midVal.Type().String(), ctrl.Interface().(IMiddleware))
				if mw, ok := ctrl.Interface().(IMiddleware); ok {
					h.Funcs = append(h.Funcs, &handle{IsFunc: false, Middleware: mw})
				}
			} else {
				//log.Dbg(midVal.Interface(), midVal.Type().String(), midVal.Interface().(IMiddleware))
				if mw, ok := midVal.Interface().(IMiddleware); ok {
					h.Funcs = append(h.Funcs, &handle{IsFunc: false, Middleware: mw})
				}
			}
		next:
		}

		// 写入handler
		// MethodByName特性取到的值receiver默认为v
		hd := h.CtrlValue.MethodByName(h.FuncName)
		if h.TransportType != HttpHandler {
			h.Funcs = append(h.Funcs, &handle{IsFunc: true, RpcFunc: hd.Interface().(func(*TRpcContext))})
		} else {
			h.Funcs = append(h.Funcs, &handle{IsFunc: true, HttpFunc: hd.Interface().(func(*THttpContext))})
		}

		// 所有的指针准备就绪后生成Interface
		// NOTE:必须初始化结构指针才能生成完整的Model,之后修改CtrlValue将不会有效果
		h.CtrlModel = h.CtrlValue.Interface()
	}

	h.Reset()
	return h
}

func (self *handler) Invoke(ctx IContext) {
	self.Pos++

	// set the controller to context vars
	if self.CtrlModel != nil {
		ctx.Data().FieldByName("controller").AsInterface(self.CtrlModel)
	}

	//
	switch self.TransportType {
	case HttpHandler:
		c := ctx.(*THttpContext)
		for self.Pos < len(self.Funcs) {
			if ctx.IsDone() {
				break
			}
			hd := self.Funcs[self.Pos]
			if hd.IsFunc {
				hd.HttpFunc(c)
			} else {
				hd.Middleware.Handler(c)
			}
			self.Pos++
		}
	case RpcHandler:
		c := ctx.(*TRpcContext)
		for self.Pos < len(self.Funcs) {
			if ctx.IsDone() {
				break
			}
			hd := self.Funcs[self.Pos]
			if hd.IsFunc {
				hd.RpcFunc(c)
			} else {
				hd.Middleware.Handler(c)
			}
			self.Pos++
		}
	case ReflectHandler:
		log.Panicf("ReflectHandler %v")
		/*
			for self.Pos < len(self.Funcs) {
				if ctx.IsDone() {
					break
				}
				v := self.Funcs[self.Pos]
				rcv := self.FuncsReceiver[self.Pos]
				if rcv.IsValid() {
					v.Call([]reflect.Value{rcv, ctx.ValueModel()})
				} else {
					v.Call([]reflect.Value{ctx.ValueModel()})
				}

				self.Pos++
			}
		*/

	}

	return
}

// 初始化
// NOTE:仅限于回收使用
func (self *handler) Reset() {
	self.Pos = -1

	// 执行中间件初始化
	for i := 0; i < len(self.Funcs); i++ {
		hd := self.Funcs[i]
		if !hd.IsFunc {
			if m, ok := hd.Middleware.(IMiddlewareRest); ok {
				m.Rest()
			}
		}
	}
}

func (self *handler) Recycle() {
	p := self.Manager.handlerPool[self.Id]
	self.Reset()
	p.Put(self)
}
