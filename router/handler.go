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

// localHandlerSeq 为本地（非 Proxy）handler 分发进程内唯一 id。
var localHandlerSeq atomic.Int64

// nextLocalHandlerId 返回一个唯一的负 id。取负是为了与 Proxy handler 用的
// 非负 crc32 id（见 generateHandlerId）分处两个区间，永不在全局 handlerModel
// 里相互覆盖。
func nextLocalHandlerId() int {
	return -int(localHandlerSeq.Add(1))
}

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

	// NOTE iota 在本 const 块内连续递增，未被重置：HandlerType 占了 0~2，
	// 因此 HttpHandler 实际是 3、RpcHandler 是 4，依此类推。两者是不同类型，
	// 不会互相误用，但**不能按下标去索引字符串数组**（见下方 String 实现）。
	HttpHandler TransportType = iota
	RpcHandler
	SubscribeHandler
	ReflectHandler // TODO废弃
)

func (self TransportType) String() string {
	switch self {
	case HttpHandler:
		return "HttpHandler"
	case RpcHandler:
		return "RpcHandler"
	case SubscribeHandler:
		return "SubscribeHandler"
	case ReflectHandler:
		return "ReflectHandler"
	default:
		return "UnknownTransport"
	}
}

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
		services      []*registry.Service //todo remove 该路由服务线路 提供网关等特殊服务用// Versions of this service
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
		input     any
		output    any
		inited    bool

		// typed Api handler：func 已在注册期构建，init 跳过 MethodByName 重绑，
		// 仅做控制器构建/中间件注入/Init/Ready，再追加 typedFunc 作主处理器。
		isTyped   bool
		typedFunc *handle // 注册期构建的 typed core（WrapHttp/WrapRpc 包装后）
	}

	handlerManager struct {
		sync.RWMutex
		handlerPool  sync.Map
		router       *TRouter
		handlerModel map[int]*handler
	}
)

// Typed handler 的可插拔编解码钩子。
//
// 默认走纯 JSON（Body().Decode / RespondByJson），上层（如 vectors 的 JSON-RPC
// 信封）可在 init 时覆盖，从而在不让 router 依赖上层 codec 的前提下保留信封协议。
//   - TypedRequestDecoder：把请求体填入 *I（指针）。
//   - TypedResponseEncoder：写出成功结果 *O。
//   - TypedErrorEncoder：写出 error。
var (
	TypedRequestDecoder = func(ctx IContext, ptr any) error {
		if b := ctx.Body(); b != nil {
			return b.Decode(ptr)
		}
		return nil
	}

	TypedResponseEncoder = func(ctx IContext, v any) {
		ctx.RespondByJson(v)
	}

	TypedErrorEncoder = func(ctx IContext, err error) {
		writeError(ctx, err)
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
	var uid int
	if handlerType == ProxyHandler {
		// Proxy handler 包裹的是共享、无状态的反向代理函数（HttpReverseProxy /
		// RpcReverseProxy），按 (service, path) 内容派生 id 并 dedup：让每 60s 的
		// registry 刷新复用同一实例、保持路由 handler id 稳定。
		uid = generateHandlerId(handlerType, tt, handlers, url, service)

		// 只查原型表，**不要用 Get** —— Get 会从对象池里弹出一个已经 init 完成的
		// 实例（funcs 已含全局链、注入的中间件、主处理器），再 Store 回去就把原型
		// 污染成了"成品"。而 clone 不拷贝 inited，于是后续克隆出来的实例 inited
		// 为 false，init 会在已完整的 funcs 上**再前置一次全局链** —— 表现为网关
		// 反代路由上的中间件执行两次，且每次 registry 刷新（60s）都可能再叠一层。
		if h := defaultHandlerManager.Model(uid); h != nil {
			//h.SetServices(services) // 更新服务列表
			return h
		}
	} else {
		// Local/Subscribe handler 往往是捕获了 per-router 状态的闭包（如 OpenAPIGroup
		// 冻结的 spec）。内容派生 id 会让同进程内多个 router 的同源闭包（PC 相同 →
		// 哈希相同）算出同一个 id，从而在全局 handlerModel 里共用一个实例、相互串扰。
		// 这里改为每次生成分发唯一 id，彻底隔离。
		uid = nextLocalHandlerId()
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

	h.TransportType = tt

	switch val := handlers[0].(type) {
	case func(*THttpContext):
		h.addMiddlewares(middlewares)
		h.funcs = append(h.funcs, &handle{IsFunc: true, HttpFunc: val})

	case func(*TRpcContext):
		h.addMiddlewares(middlewares)
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

			// 第一个参数：context 类型
			if handlerT.In(1) != ContextType && handlerT.In(1) != HttpContextType && handlerT.In(1) != RpcContextType {
				log.Panicf("the handler %s must have Context as the second argument!", h.Name)
			}

			/*
				// 第二个参数：必须是指针
				if handlerT.In(2) != nil && handlerT.In(2).Kind() != reflect.Ptr {
					log.Panicf("the handler %s must have Context as the second argument!", h.Name)
				}

				// 第一个返回值：必须是指针
				if handlerT.Out(0) != nil && handlerT.Out(0).Kind() != reflect.Ptr {
					log.Panicf("the handler %s must have Context as the second argument!", h.Name)
				}
			*/
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

		h.addMiddlewares(middlewares)

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
	self.handlerModel[h.Id] = h
}

// Model 只查原型表，不碰对象池。
//
// 与 Get 的区别很关键：Get 会优先从池里弹出一个**已 init 完成**的实例，
// 那是给请求用的成品，不是原型。想判断"某个 id 是否已登记过"必须用 Model ——
// 用 Get 拿到成品再 Store 回去会把原型污染成成品，而 clone 不拷贝 inited，
// 后续克隆就会在已完整的 funcs 上重复跑一遍 init。
func (self *handlerManager) Model(id int) *handler {
	self.RLock()
	defer self.RUnlock()
	return self.handlerModel[id]
}

func (self *handlerManager) Get(id int) *handler {
	if v, ok := self.handlerPool.Load(id); ok {
		// 守卫：handlerPool 仅本包写入 StackCache，但若 sync.Map 被外部污染
		// 也不应直接 panic 拖崩 router；miss-typed entry 走 fallback 路径。
		if c, ok := v.(memory.StackCache); ok {
			if h := c.Pop(); h != nil {
				if hd, ok := h.(*handler); ok {
					return hd
				}
			}
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
		if cached, typeOk := v.(memory.StackCache); typeOk {
			c = cached
		}
	}
	if c == nil {
		// LoadOrStore 收口首放竞态：原先 Load-检查-Store 两步下，并发首放会各建
		// 一个 StackCache 且后写覆盖前写——输家的 cache（NewStack 会起 vaccuum
		// GC goroutine）被静默丢弃造成 goroutine 泄漏。现在输家显式 Close。
		fresh := memory.NewStack(
			memory.WithInterval(60),
			memory.WithExpire(3600),
		) // cacher
		if v, loaded := self.handlerPool.LoadOrStore(id, fresh); loaded {
			if closer, ok := fresh.(interface{ Close() error }); ok {
				_ = closer.Close()
			}
			c, _ = v.(memory.StackCache)
		} else {
			c = fresh
		}
	}

	if c != nil {
		c.Push(h)
	}
}

func (self *handler) Services() []*registry.Service {
	self.RLock()
	defer self.RUnlock()
	return self.services
}

func (self *handler) Service() string {
	return self.service
}

func (self *handler) SetServices(services []*registry.Service) {
	self.Lock()
	defer self.Unlock()
	self.services = services
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

	// 全局链最先执行：插到 funcs 最前面，排在路由级 mids、控制器内嵌中间件、
	// 主控制器之前。放在 init（而非注册期）有个好处 —— init 是首个请求才触发的
	// 懒初始化，所以 Use() 写在 Url() 前后都生效，不存在"Use 必须先于路由注册"
	// 这类顺序陷阱。
	if router != nil {
		self.prependMiddlewares(router.Middlewares())
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

		// typed handler 的主 func 已在注册期构建（typedFunc），跳过 MethodByName 反射重绑；
		// 它在控制器/中间件注入完成后于下方统一追加。
		if !self.isTyped {
			// 写入handler
			// MethodByName特性取到的值receiver默认为v
			hd := ctrl.MethodByName(self.FuncName)
			if !hd.IsValid() {
				log.Errf("Method %s not found in controller %s", self.FuncName, self.ctrlName)
				// 不能直接 fall through —— hd.Type().In(0) 在 invalid Value 上会 panic。
				// 跳过 handler 切片填充，仅保留中间件初始化（下方循环）。
				goto initMiddlewares
			}

			var h *handle
			switch hd.Type().In(0) {
			case ContextType:
				if fn, ok := hd.Interface().(func(IContext)); ok {
					if self.TransportType != HttpHandler {
						h = &handle{IsFunc: true, RpcFunc: WrapRpc(fn)}
					} else {
						h = &handle{IsFunc: true, HttpFunc: WrapHttp(fn)}
					}
				}

				// func(CtxType, *In) (*Out)
				if hd.Type().NumIn() > 3 && hd.Type().NumOut() == 1 {
					h = &handle{IsFunc: true}
					if self.TransportType != HttpHandler {
						h.RpcFunc = hd.Interface().(func(*TRpcContext))
					} else {
						h.HttpFunc = hd.Interface().(func(*THttpContext))
					}
				}

			case RpcContextType:
				h = &handle{IsFunc: true, RpcFunc: hd.Interface().(func(*TRpcContext))}
			case HttpContextType:
				h = &handle{IsFunc: true, HttpFunc: hd.Interface().(func(*THttpContext))}
			default:
				log.Errf("the handler %v is not supportable!", hd)
			}

			self.funcs = append(self.funcs, h)
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

	// typed 主 func 排在 explicit mids + 注入的 ctrl mids 之后，作为最后的处理器。
	if self.isTyped && self.typedFunc != nil {
		self.funcs = append(self.funcs, self.typedFunc)
	}

initMiddlewares:
	/* 初始化中间件 */
	//
	// MiddlewareCreator 到这一步才能实例化（需要 router），因此其传输适配性也在
	// 这里判定：中间件实现 IMiddlewareTransport 声明了自己只适用于某一侧时，
	// 从本 handler 的链上原地摘除 —— 不适用的一侧运行期不会看到该条目。
	// 不实现该接口的默认两侧都装（IMiddleware.Handler 收 IContext，天然协议无关）。
	if len(self.funcs) > 0 {
		kept := self.funcs[:0] // 原地过滤，不额外分配
		for _, handle := range self.funcs {
			if handle.Middleware == nil && handle.MiddlewareCreator != nil {
				middleware := handle.MiddlewareCreator(router)
				if !middlewareSupports(middleware, self.TransportType) {
					log.Dbgf("middleware %T 未声明支持 %v，已从 handler %s 的链上跳过",
						middleware, self.TransportType, self.Name)
					continue
				}
				handle.Middleware = middleware
				handle.HttpFunc = WrapHttp(middleware.Handler)
				handle.RpcFunc = WrapRpc(middleware.Handler)
			}
			kept = append(kept, handle)
		}
		self.funcs = kept
	}

	self.reset()
	self.inited = true
}

// middlewareSupports 判定中间件是否适用于给定传输类型。
// 未实现 IMiddlewareTransport 视为通用（两侧都装）。
func middlewareSupports(mw IMiddleware, tt TransportType) bool {
	decl, ok := mw.(IMiddlewareTransport)
	if !ok {
		return true
	}

	supported := decl.Transports()
	if len(supported) == 0 {
		// 声明了接口却返回空集视为配置错误。按"通用"处理而非全部摘除 ——
		// 静默丢掉一个用户明确装上的中间件，比多装一次危险得多。
		log.Warnf("middleware %T 的 Transports() 返回空集，按通用中间件处理", mw)
		return true
	}

	for _, t := range supported {
		if t == tt {
			return true
		}
	}
	return false
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
		isTyped:       self.isTyped,
		typedFunc:     self.typedFunc,
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
//
// NOTE 这个循环长成这样是**实测的结果**，不要"顺手优化"。
//
// 直觉上 TransportType 与 ctx 的具体类型在整个 handler 生命周期内都是常量，
// 把 switch 和类型断言提到循环外似乎稳赚。实测（handler_bench_test.go，
// 50 中间件长链，-count=5 取中位数）恰恰相反：
//
//	循环内断言（本实现）    4852 ns/op
//	提到循环外 + 闭包       5567 ns/op   +15%
//	提到循环外 + 展开两份   5040 ns/op   +4%
//
// 原因是对具体指针类型的断言在 Go 里只是一次类型字比较，分支预测几乎必中，
// 本来就不值钱；而提取出去要么引入闭包间接调用，要么把循环体拆成两份影响
// 指令缓存，代价都比省下的多。
//
// pos 是 handler 上的原子量而非局部变量：中间件调用 ctx.Next() 会重入本函数，
// 靠共享的 pos 接着往下走，不能改成局部索引。
func (self *handler) invokeHandlers(ctx IContext) error {
	ln := len(self.funcs)
	for {
		pos := int(self.pos.Load())
		if pos >= ln || ctx.IsDone() {
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
				return errors.New("ReflectHandler not implemented")
			default:
				return fmt.Errorf("unknown transport type %v", self.TransportType)
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

	if err := self.invokeHandlers(ctx); err != nil {
		// 不能 panic：invokeHandlers 在 context 类型错配等运行时场景会返回错误，
		// 让 panic 逃出去会拉崩整个 server goroutine。改为记录日志、丢弃本次请求。
		log.Errf("handler %s invoke error: %v", self.Name, err)
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

// addMiddlewares 把 middlewares 追加到 self.funcs（按 self.TransportType 选择 Http/Rpc 包装）。
//
// 本框架同时承载 HTTP 与 RPC，两者的 handler 是各自独立的对象（handler id 即含
// TransportType），因此中间件必须在**装配期**按传输类型筛掉不适用的，让不适用的
// 一侧 funcs 里根本不存在该条目 —— 请求路径上零开销。
//
// 参数类型本身就携带了传输意图：func(*THttpContext) 只可能对 HTTP 有意义。
// 此前不做筛选，把 HTTP 中间件挂到 RPC 路由上时注册期毫无提示，运行期
// invokeHandlers 走 RPC 分支去调 nil 的 RpcFunc —— 每个请求都 nil panic，
// 默认 Recover 开着所以表现为"这条 RPC 路由恒定 500"，日志里看不出是中间件挂错边。
//
// 返回 func(IRouter) IMiddleware 的中间件此刻还没实例化（需要 router），其传输
// 适配性推迟到 init 的 initMiddlewares 段按 IMiddlewareTransport 判定。
func (self *handler) addMiddlewares(middlewares []any) {
	self.funcs = append(self.funcs, self.buildMiddlewares(middlewares)...)
}

// prependMiddlewares 把 middlewares 插到 self.funcs 最前面。
// 供全局链使用 —— 全局中间件必须早于路由级、控制器内嵌的中间件执行。
func (self *handler) prependMiddlewares(middlewares []any) {
	built := self.buildMiddlewares(middlewares)
	if len(built) == 0 {
		return
	}
	self.funcs = append(built, self.funcs...)
}

// buildMiddlewares 把中间件声明按本 handler 的传输类型编译成 handle 列表，
// 不适用的直接筛掉（见 addMiddlewares 的说明）。
func (self *handler) buildMiddlewares(middlewares []any) []*handle {
	if len(middlewares) == 0 {
		return nil
	}

	isHttp := self.TransportType == HttpHandler
	built := make([]*handle, 0, len(middlewares))

	for _, mid := range middlewares {
		switch v := mid.(type) {
		case func(IContext):
			// 协议无关，两侧都装，按本 handler 的传输类型包装。
			if isHttp {
				built = append(built, &handle{IsFunc: true, HttpFunc: WrapHttp(v)})
			} else {
				built = append(built, &handle{IsFunc: true, RpcFunc: WrapRpc(v)})
			}
		case func(*THttpContext):
			if !isHttp {
				log.Dbgf("middleware %s 是 HTTP 专属，已从 RPC handler %s 的链上跳过",
					GetFuncName(v, filepath.Separator), self.Name)
				continue
			}
			built = append(built, &handle{IsFunc: true, HttpFunc: v})
		case func(*TRpcContext):
			if isHttp {
				log.Dbgf("middleware %s 是 RPC 专属，已从 HTTP handler %s 的链上跳过",
					GetFuncName(v, filepath.Separator), self.Name)
				continue
			}
			built = append(built, &handle{IsFunc: true, RpcFunc: v})
		case func(IRouter) IMiddleware:
			built = append(built, &handle{IsFunc: true, MiddlewareCreator: v})
		default:
			log.Errf("unknow middleware %v", v)
		}
	}

	return built
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
