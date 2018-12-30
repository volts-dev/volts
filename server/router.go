package server

import (
	"errors"
	"fmt"
	"io"
	nethttp "net/http"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"time"
	"vectors/volts/codec"
	"vectors/volts/protocol"
	"vectors/volts/server/listener/http"
	"vectors/volts/server/listener/rpc"

	log "vectors/logger"

	"github.com/VectorsOrigin/template"
	"github.com/VectorsOrigin/utils"
)

// Can connect to RPC service using HTTP CONNECT to rpcPath.
var connected = "200 Connected to RPC"

type (
	TRouter struct {
		sync.RWMutex
		*TemplateVar
		handlerMapMu sync.RWMutex
		handlerMap   map[string]*TModule

		// TODO 修改进其他包 或者独立出来

		readTimeout  time.Duration
		writeTimeout time.Duration
		show_route   bool

		//msgPool    sync.Pool
		objectPool     *TPool
		webHandlerPool sync.Pool
		rpcHandlerPool sync.Pool
		respPool       sync.Pool

		server     *TServer
		tree       *TTree
		middleware *TMiddlewareManager // 中间件
		template   *template.TTemplateSet
		//templateVar map[string]interface{} // TODO 全局变量. 需改进
	}
)

func NewRouter() *TRouter {
	tree := NewRouteTree()
	tree.IgnoreCase = true
	tree.DelimitChar = '.' // 修改为xxx.xxx

	router := &TRouter{
		tree:       tree,
		handlerMap: make(map[string]*TModule),
		objectPool: NewPool(),
		//handlerPool: NewPool(),
		middleware: NewMiddlewareManager(),
		template:   template.NewTemplateSet(),
		//templateVar: make(map[string]interface{}),
		TemplateVar: NewTemplateVar(),
	}

	//router.GVar["Version"] = ROUTER_VER
	router.templateVar["StratDateTime"] = time.Now().UTC()

	/*
		router.msgPool.New = func() interface{} {

			header := message.Header([12]byte{})
			header[0] = message.MagicNumber

			return &message.TMessage{
				Header: &header,
			}

		}
	*/
	router.webHandlerPool.New = func() interface{} {
		return NewWebHandler(router)
	}

	router.rpcHandlerPool.New = func() interface{} {
		return NewRpcHandler(router)
	}

	// inite HandlerPool New function
	router.respPool.New = func() interface{} {
		return http.NewResponser()
	}

	return router
}

// init when the router is active
func (self *TRouter) init() {
	if self.server.Config.PrintRouterTree {
		self.tree.PrintTrees()
	}

	// init middleware
	for _, name := range self.middleware.Names {
		ml := self.middleware.Get(name)
		if m, ok := ml.(IMiddlewareInit); ok {
			m.Init(self)
		}

	}
}

// return the server
func (self *TRouter) Server() *TServer {
	return self.server
}

func (self *TRouter) RegisterModule(mod IModule, build_path ...bool) {
	if mod == nil {
		self.server.logger.Warn("RegisterModule is nil")
		return
	}

	self.tree.Conbine(mod.GetRoutes())

	utils.MergeMaps(mod.GetTemplateVar(), self.templateVar)
}

// 注册中间件
func (self *TRouter) RegisterMiddleware(mod ...IMiddleware) {
	for _, m := range mod {
		lType := reflect.TypeOf(m)
		if lType.Kind() == reflect.Ptr {
			lType = lType.Elem()
		}
		lName := lType.String()
		self.middleware.Add(lName, m)
	}

}

// TODO 优化 route the midware request,response,panic
func (self *TRouter) routeMiddleware(method string, route *TRoute, handler IHandler, c *TController, ctrl reflect.Value) {
	// Action结构外的其他中间件
	var (
		isFound bool
		//fn reflect.Value
		mid_val, mid_ptr_val reflect.Value
		mid_typ              reflect.Type
		mid_name             string // name of middleware
		mid_itf              interface{}
	)

	name_lst := make(map[string]bool) // TODO　不用MAP list of midware found it ctrl
	for key, ml := range self.middleware.middlewares {
		// @:直接返回 放弃剩下的Handler
		if handler.IsDone() {
			name_lst = nil // not report
			break
		}

		// 继续
		isFound = false
		//TODO 优化遍历
		// TODO 有待优化 可以缓存For结果
		for i := 0; i < ctrl.NumField(); i++ { // Action结构下的中间件
			mid_val = ctrl.Field(i) // 获得成员
			mid_typ = mid_val.Type()

			// 过滤继承结构的中间件
			//if lField.Kind() == reflect.Struct {
			//	continue
			//}

			// get the name of middleware from the Type
			if mid_typ.Kind() == reflect.Ptr {
				mid_typ = mid_typ.Elem()
			}

			mid_name = mid_typ.String()
			log.Dbg("afsdf", mid_name, key, mid_name == key)
			if mid_name == key {
				name_lst[mid_name] = true // mark it as found

				if mid_val.Kind() == reflect.Struct {
					//	过滤继承的结构体
					//	type TAction struct {
					//		TEvent
					//	}
					// TODO 优化去除Call 但是中间件必须是ctrl 上的而非中间件管理器的
					if method == "request" {
						//fn = mid_val.MethodByName("Request")
						if m, ok := ml.(IMiddlewareRequest); ok {
							m.Request(ctrl.Interface(), c)
						}
					} else if method == "response" {
						//fn = mid_val.MethodByName("Response")
						if m, ok := ml.(IMiddlewareResponse); ok {
							m.Response(ctrl.Interface(), c)
						}
					} else if method == "panic" {
						//fn = mid_val.MethodByName("Panic")
						if m, ok := ml.(IMiddlewarePanic); ok {
							m.Panic(ctrl.Interface(), c)
						}
					}

					//if fn.IsValid() {
					//	fn.Call([]reflect.Value{ctrl, reflect.ValueOf(c)}) //执行方法
					//}
				} else if mid_val.Kind() != reflect.Struct && mid_val.IsNil() {
					mid_itf, mid_ptr_val = cloneInterfacePtrFeild(ml) // 克隆
					if method == "request" {
						if m, ok := mid_itf.(IMiddlewareRequest); ok {
							m.Request(ctrl.Interface(), c)
						}

					} else if method == "response" {
						if m, ok := ml.(IMiddlewareResponse); ok {
							m.Response(ctrl.Interface(), c)
						}

					} else if method == "panic" {
						if m, ok := mid_itf.(IMiddlewarePanic); ok {
							m.Panic(ctrl.Interface(), c)
						}
					}
					//mid_ptr_val := reflect.ValueOf(mid_itf) // or reflect.ValueOf(lMiddleware).Convert(mid_val.Type())

					// set back the middleware pointer to the controller
					if mid_val.Kind() == mid_ptr_val.Kind() {
						mid_val.Set(mid_ptr_val) // TODO Warm: Field must exportable
					}

				}
				// STEP:结束循环
				isFound = true
				break
			} else {
				name_lst[mid_name] = false
			}
		}

		// invoke the minddleware which not use in the controller
		// 更新非控制器中的中间件
		if !isFound {
			//Warn(" routeBefore not isFound", key, ctrl.Interface(), handler)
			if method == "request" {
				if m, ok := ml.(IMiddlewareRequest); ok {
					m.Request(ctrl.Interface(), c)
				}
			} else if method == "response" {
				if m, ok := ml.(IMiddlewareResponse); ok {
					m.Response(ctrl.Interface(), c)
				}
			} else if method == "panic" {
				if m, ok := ml.(IMiddlewarePanic); ok {
					m.Panic(ctrl.Interface(), c)
				}
			}
		}
	}

	// report the name of midware which on controller but not register in the server
	for name, found := range name_lst {
		if !found {
			self.server.logger.Errf("%v isn't be register in controller %v", name, ctrl.String())
		}
	}
}

// TODO 有待优化
// 执行静态文件路由
func (self *TRouter) routeHttpStatic(req *nethttp.Request, w *http.TResponseWriter) {
	var lFilePath string
	lPath, lFileName := filepath.Split(req.URL.Path) //products/js/base.js
	//urlPath := strings.Split(strings.Trim(req.URL.Path, `/`), `/`) // Split不能去除/products

	//根目录静态文件映射过滤
	if lPath == "/" {
		switch filepath.Ext(lFileName) {
		case ".txt", ".html", ".htm": // 目前只开放这种格式
			lFilePath = filepath.Join(lFileName)
		}

	} else {
		//urlPath := strings.Trim(req.URL.Path, `/`)

		//如果第一个是静态文件夹名则选用主静态文件夹,反之使用模块
		// /static/js/base.js
		// /ModuleName/static/js/base.js
		lDirs := strings.Split(lPath, "/")
		if strings.EqualFold(lDirs[1], STATIC_DIR) {
			//if strings.HasPrefix(lPath, "/"+STATIC_DIR) { // 如果请求是 /Static/js/base.js
			/* static_file = filepath.Join(
			self.Server.Config.RootPath,                           // c:\project\
			STATIC_DIR,                          // c:\project\static\
			strings.Join(urlPath[1:], string(filepath.Separator)), // c:\project\static\js\base.js
			fileName)
			*/

			lFilePath = filepath.Join(req.URL.Path)

		} else { // 如果请求是 products/Static/js/base.js
			/* static_file = filepath.Join(
			self.Server.Config.RootPath,                           // c:\project\
			MODULE_DIR,                         // c:\project\Modules
			urlPath[0],                                            // c:\project\Modules\products\
			STATIC_DIR,                          // c:\project\Modules\products\static\
			strings.Join(urlPath[1:], string(filepath.Separator)), // c:\project\Modules\products\static\js\base.js
			fileName)
			*/

			//Debug("lDirsD", lDirs, STATIC_DIR, string(os.PathSeparator))
			// 再次检查 Module Name 后必须是 /static 目录
			if strings.EqualFold(lDirs[2], STATIC_DIR) {
				lFilePath = filepath.Join(
					MODULE_DIR, // c:\project\Modules
					req.URL.Path)
			} else {
				nethttp.NotFound(w, req)
				return

			}
		}
	}

	// 当模块路径无该文件时，改为程序static文件夹
	if !utils.FileExists(lFilePath) {
		lIndex := strings.Index(lFilePath, STATIC_DIR)
		if lIndex != -1 {
			lFilePath = lFilePath[lIndex-1:]

		}
	}

	//Info("static_file", static_file)
	if req.Method == "GET" || req.Method == "HEAD" {
		lFilePath = filepath.Join(
			//self.Server.Config.RootPath,
			AppPath,
			lFilePath)
		// 当程序文件夹无该文件时
		if !utils.FileExists(lFilePath) {
			//self.Logger.DbgLn("Not Found", lFilePath)
			nethttp.NotFound(w, req)
			return
		}
		//Noted: ServeFile() can not accept "/AA.exe" string, only accepy "AA.exe" string.
		nethttp.ServeFile(w, req, lFilePath) // func ServeFile(w ResponseWriter, r *Request, name string)
		//self.Server.Logger.Println("RouteFile:" + static_file)
		return
	}
	//Debug("RouteStatic", path, fileName)
	return
}

func (self *TRouter) ServeTCP(w rpc.Response, req *rpc.Request) {
	self.routeRpc(w, req)
}

func (self *TRouter) ServeHTTP(w nethttp.ResponseWriter, req *nethttp.Request) {
	if req.Method == "CONNECT" { // serve as a raw network server
		conn, _, err := w.(nethttp.Hijacker).Hijack()
		if err != nil {
			self.server.logger.Infof("rpc hijacking %v:%v", req.RemoteAddr, ": ", err.Error())
			return
		}
		io.WriteString(conn, "HTTP/1.0 "+connected+"\n\n")
		/*
			s.mu.Lock()
			s.activeConn[conn] = struct{}{}
			s.mu.Unlock()
		*/

		msg, err := protocol.Read(conn)
		if err != nil {
			self.server.logger.Info("rpc Read ", err.Error())
			return
		}

		r := rpc.NewRequest(msg, req.Context())
		self.ServeTCP(w, r)

	} else { // serve as a web server

		// Pool 提供TResponseWriter
		rsp := self.respPool.Get().(*http.TResponseWriter)
		rsp.Connect(w)
		self.routeHttp(req, rsp)
		// Pool 回收TResponseWriter
		rsp.ResponseWriter = nil
		self.respPool.Put(rsp)
	}
}

//
func (self *TRouter) callCtrl(route *TRoute, ct *TController, handler IHandler) {
	var (
		args          []reflect.Value //handler参数
		lActionVal    reflect.Value
		lActionTyp    reflect.Type
		parm          reflect.Type
		CtrlValidable bool
	)

	// TODO:将所有需要执行的Handler 存疑列表或者树-Node保存函数和参数
	//logger.Dbg("parm %s %d:%d %p %p", handler.TemplateSrc, lRoute.Action, lRoute.MainCtrl, len(lRoute.Ctrls), lRoute.Ctrls)
	for _, ctrl := range route.Ctrls {
		// stop runing ctrl
		if handler.IsDone() {
			break
		}

		//handler.ControllerIndex = index //index
		// STEP#: 获取<Ctrl.Func()>方法的参数
		for i := 0; i < ctrl.FuncType.NumIn(); i++ {
			parm = ctrl.FuncType.In(i) // 获得参数

			//log.Dbg("aaa", parm.String(), handler.TypeModel().String())
			switch parm { //arg0.Elem() { //获得Handler的第一个参数类型.
			case handler.TypeModel(): // TODO 优化调用 // if is a pointer of TWebHandler
				{
					args = append(args, handler.ValueModel()) // 这里将传递本函数先前创建的handle 给请求函数
				}
			default:
				{
					//
					log.Dbg("aaa", parm.Kind())
					if i == 0 && parm.Kind() == reflect.Struct { // 第一个 //第一个是方法的结构自己本身 例：(self TMiddleware) ProcessRequest（）的 self
						lActionTyp = parm
						lActionVal = self.objectPool.Get(parm)
						if lActionVal.Kind() == reflect.Ptr {
							lActionVal = lActionVal.Elem()
						}
						// lActionVal 由类生成实体值,必须指针转换而成才是Addressable  错误：lVal := reflect.Zero(aHandleType)
						args = append(args, lActionVal) //插入该类型空值
						break
					}

					// TODO优化判断  插入RPC 参数和返回
					if rpc_handler := ct.GetRpcHandler(); rpc_handler != nil {

						if i == 2 {
							args = append(args, rpc_handler.argv) //插入该类型空值
						}
						if i == 3 {
							args = append(args, rpc_handler.replyv) //插入该类型空值
						}
						break
					}

					/* TODO 由于接口冲突故考虑放弃支持 handler.Request，handler.Response
					// STEP:如果是参数是 http.ResponseWriter 值
					if strings.EqualFold(parm.String(), "http.ResponseWriter") { // Response 类
						//args = append(args, reflect.ValueOf(w.ResponseWriter))
						args = append(args, reflect.ValueOf(handler.Response()))
						break
					}

					// STEP:如果是参数是 http.Request 值
					if parm == reflect.TypeOf(handler.Request()) { // request 指针
						args = append(args, reflect.ValueOf(handler.Request())) //TODO (同上简化reflect.ValueOf）
						break
					}
					*/

					// by default append a zero value
					args = append(args, reflect.Zero(parm)) //插入该类型空值
				}
			}
		}

		CtrlValidable = lActionVal.IsValid()
		if CtrlValidable {
			//self.Logger.Info("routeBefore")
			self.routeMiddleware("request", route, handler, ct, lActionVal)
		}
		//logger.Infof("safelyCall %v ,%v", handler.Response.Written(), args)
		if !handler.IsDone() {
			//self.Logger.Info("safelyCall")
			// -- execute Handler or Panic Event
			self.safelyCall(ctrl.Func, args, route, handler, ct, lActionVal) //传递参数给函数.<<<
		}

		if !handler.IsDone() && CtrlValidable {
			// # after route
			self.routeMiddleware("response", route, handler, ct, lActionVal)
		}

		if CtrlValidable {
			self.objectPool.Put(lActionTyp, lActionVal)
		}
	}
}

func (self *TRouter) safelyCall(function reflect.Value, args []reflect.Value, route *TRoute, handler IHandler, ct *TController, ctrl reflect.Value) {
	defer func() {
		if err := recover(); err != nil {
			if self.server.Config.RecoverPanic { //是否绕过错误处理直接关闭程序
				self.routeMiddleware("panic", route, handler, ct, ctrl)

				for i := 1; ; i++ {
					_, file, line, ok := runtime.Caller(i)
					if !ok {
						break
					}
					log.Err(file, line)
				}
			} else {
				panic(err)
			}
		}
	}()

	// TODO 优化速度GO2
	// Invoke the method, providing a new value for the reply.
	function.Call(args)
}

// the API for RPC
func (self *TRouter) ConnectBroke(w rpc.Response, req *rpc.Request) {
	//self.routeMiddleware("disconnected", route, handler, ct, lActionVal)
}

func (self *TRouter) routeRpc(w rpc.Response, req *rpc.Request) {
	// 心跳包 直接返回
	if req.Message.IsHeartbeat() {
		//data := req.Message.Encode()
		//w.Write(data)
		//TODO 补全状态吗
		return
	}

	var (
		err error
	)

	resMetadata := make(map[string]string)
	//newCtx := context.WithValue(context.WithValue(ctx, share.ReqMetaDataKey, req.Metadata),
	//	share.ResMetaDataKey, resMetadata)

	//		ctx := context.WithValue(context.Background(), RemoteConnContextKey, conn)
	msg := req.Message // return the packet struct
	serviceName := msg.ServicePath
	//methodName := msg.ServiceMethod

	// TODO 无需克隆
	resraw := protocol.GetMessageFromPool()
	res := msg.CloneTo(resraw)

	//protocol.PutMessageToPool(resraw)
	res.SetMessageType(protocol.Response)
	// 匹配路由树
	//route, _ := self.tree.Match("HEAD", msg.ServicePath+"."+msg.ServiceMethod)
	var coder codec.ICodec
	var handler *TRpcHandler
	route, _ := self.tree.Match("CONNECT", msg.Path)
	if route == nil {
		err = errors.New("rpc: can't match route " + serviceName)
		handleError(res, err)
	} else {
		// 获取支持的序列模式
		coder = codec.Codecs[msg.SerializeType()]
		if coder == nil {
			err = fmt.Errorf("can not find codec for %d", msg.SerializeType())
			handleError(res, err)

		} else {
			// 获取RPC参数
			// 获取控制器参数类型
			var argv = self.objectPool.Get(route.MainCtrl.ArgType)
			err = coder.Decode(msg.Payload, argv.Interface()) //反序列化获得参数值

			if err != nil {
				handleError(res, err)
			} else {
				handler = self.rpcHandlerPool.Get().(*TRpcHandler)
				handler.reset(w, req, self, route)
				handler.argv = argv
				handler.replyv = self.objectPool.Get(route.MainCtrl.ReplyType)

				// 执行控制器
				self.callCtrl(route, &TController{rpcHandler: handler}, handler)
			}
		}
	}

	// 返回数据
	if !msg.IsOneway() {
		// 序列化数据
		data, err := coder.Encode(handler.replyv.Interface())
		//argsReplyPools.Put(mtype.ReplyType, replyv)
		if err != nil {
			handleError(res, err)
			return
		}
		res.Payload = data

		if len(resMetadata) > 0 { //copy meta in context to request
			meta := res.Metadata
			if meta == nil {
				res.Metadata = resMetadata
			} else {
				for k, v := range resMetadata {
					meta[k] = v
				}
			}
		}

		var cnt int
		cnt, err = w.Write(res.Payload)
		if err != nil {
			log.Dbg(err.Error())
		}
		log.Dbg("aa", cnt, string(res.Payload))
	}

	return
}

/*
// 处理URL 请求
// 优化处理
#Pool Route/ResponseWriter
*/
func (self *TRouter) routeHttp(req *nethttp.Request, w *http.TResponseWriter) {
	lPath := req.URL.Path //获得的地址
	//ar lRoute *TRoute
	//ar lParam Params
	// # match route from tree
	lRoute, lParam := self.tree.Match(req.Method, lPath)
	if self.show_route {
		self.server.logger.Info("[Path]%v [Route]%v", lPath, lRoute.FilePath)
	}

	if lRoute == nil {
		self.routeHttpStatic(req, w) // # serve as a static file link
		return
	}
	/*
		if lRoute.isReverseProxy {
			self.routeProxy(lRoute, lParam, req, w)

			return
		}
	*/

	// # init Handler
	handler := self.webHandlerPool.Get().(*TWebHandler)
	handler.reset(w, req, self, lRoute)

	// init dy url parm to handler
	for _, param := range lParam {
		handler.setPathParams(param.Name, param.Value)
	}

	self.callCtrl(lRoute, &TController{webHandler: handler}, handler)

	if handler.finalCall != nil {
		//if handler.finalCall.IsValid() {
		//if f, ok := handler.finalCall.Interface().(func(*TWebHandler)); ok {
		//f([]reflect.Value{reflect.ValueOf(lHandler)})
		handler.finalCall(handler)
		//			Trace("Handler Final Call")
		//}
	}

	//##################
	//设置某些默认头
	//设置默认的 content-type
	//TODO 由Tree完成
	//tm := time.Now().UTC()
	handler.SetHeader(true, "Engine", "Volts") //取当前时间
	//handler.SetHeader(true, "Date", WebTime(tm)) //
	//handler.SetHeader(true, "Content-Type", "text/html; charset=utf-8")
	if handler.TemplateSrc != "" {
		//添加[static]静态文件路径
		// log.Dbg(STATIC_DIR, path.Join(utils.FilePathToPath(lRoute.FilePath), STATIC_DIR))
		handler.templateVar[STATIC_DIR] = path.Join(utils.FilePathToPath(lRoute.FilePath), STATIC_DIR)
	}

	// 结束Route并返回内容
	handler.Apply()

	self.webHandlerPool.Put(handler) // Pool 回收Handler

	return
}

func handleError(res *protocol.TMessage, err error) (*protocol.TMessage, error) {
	res.SetMessageStatusType(protocol.Error)
	if res.Metadata == nil {
		res.Metadata = make(map[string]string)
	}
	res.Metadata["__rpcx_error__"] = err.Error()
	return res, err
}

// 克隆interface 并复制里面的指针
func cloneInterfacePtrFeild(s interface{}) (interface{}, reflect.Value) {
	lVal := reflect.Indirect(reflect.ValueOf(s)) //Indirect 等同 Elem()
	lType := reflect.TypeOf(s).Elem()            // 返回类型
	lrVal := reflect.New(lType)                  //创建某类型
	lrVal.Elem().Set(lVal)
	/*
		for i := 0; i < lVal.NumField(); i++ {
			lField := lVal.Field(i)
			Warn("jj", lField, lField.Kind())
			if lField.Kind() == reflect.Ptr {
				//fmt.Println("jj", lField, lField.Elem())
				//lrVal.Field(i).SetPointer(unsafe.Pointer(lField.Pointer()))
				lrVal.Elem().Field(i).Set(lField)
				//lrVal.FieldByName("Id").SetString("fasd")
			}
		}
	*/
	//fmt.Println(lrVal)
	//return reflect.Indirect(lrVal).Interface()
	return lrVal.Interface(), lrVal
}
