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

	log "github.com/volts-dev/logger"
	"github.com/volts-dev/template"
	"github.com/volts-dev/utils"
	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/protocol"
	"github.com/volts-dev/volts/server/listener/http"
	"github.com/volts-dev/volts/server/listener/rpc"
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

// register module
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
		typ := reflect.TypeOf(m)
		if typ.Kind() == reflect.Ptr {
			typ = typ.Elem()
		}
		name := typ.String()
		self.middleware.Add(name, m)
	}
}

// the order is according by controller for modular register middleware.
// TODO 优化遍历
// TODO 优化 route the midware request,response,panic
func (self *TRouter) routeMiddleware(method string, route *TRoute, handler IHandler, c *TController, ctrl reflect.Value) {
	var (
		mid_val, mid_ptr_val reflect.Value
		mid_typ              reflect.Type
		mid_name             string // name of middleware
		mid_itf              interface{}
	)

	// the minddleware list from the controller
	name_lst := make(map[string]bool)      // TODO　不用MAP list of midware found it ctrl
	for i := 0; i < ctrl.NumField(); i++ { // middlewares under controller
		// @:直接返回 放弃剩下的Handler
		if handler.IsDone() {
			name_lst = nil // not report
			break          // igonre the following any controls
		}

		mid_val = ctrl.Field(i)  // get the middleware value
		mid_typ = mid_val.Type() // get the middleware type

		if mid_typ.Kind() == reflect.Ptr {
			mid_typ = mid_typ.Elem()
		}

		// get the name of middleware from the Type
		mid_name = mid_typ.String()

		ml := self.middleware.Get(mid_name)
		if ml == nil {
			// normall only struct and pointer could be a middleware
			if mid_val.Kind() == reflect.Struct || mid_val.Kind() == reflect.Ptr {
				name_lst[mid_name] = false
			}
		} else {
			name_lst[mid_name] = true

			if mid_val.Kind() == reflect.Ptr {
				//	过滤指针中间件
				//	type Controller struct {
				//		Session *TSession
				//	}

				// all middleware are nil at first time on the controller
				if mid_val.IsNil() {
					mid_ptr_val = cloneInterfacePtrFeild(ml) // TODO 优化克隆

					// set back the middleware pointer to the controller
					if mid_val.Kind() == mid_ptr_val.Kind() {
						mid_val.Set(mid_ptr_val) // TODO Warm: Field must exportable
					}
				}
			}

			// call api
			if method == "request" {
				if m, ok := mid_val.Interface().(IMiddlewareRequest); ok {
					m.Request(ctrl.Interface(), c)
				}

			} else if method == "response" {
				if m, ok := mid_val.Interface().(IMiddlewareResponse); ok {
					m.Response(ctrl.Interface(), c)
				}

			} else if method == "panic" {
				if m, ok := mid_val.Interface().(IMiddlewarePanic); ok {
					m.Panic(ctrl.Interface(), c)
				}
			}
		}
	}

	// invoke the minddleware which not use in the controller
	// 更新非控制器中的中间件
	// TODO 暂停 由于部分中间件在某些控制器不应该給执行而被执行
	/*
		for key, ml := range self.middleware.middlewares {
			if _, has := name_lst[key]; !has {
				// @:直接返回 放弃剩下的Handler
				if handler.IsDone() {
					break
				}

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
	*/
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
	if req.Method == "GET" || req.Method == "HEAD" {
		path, file_Name := filepath.Split(req.URL.Path) //products/js/base.js
		//urlPath := strings.Split(strings.Trim(req.URL.Path, `/`), `/`) // Split不能去除/products

		//根目录静态文件映射过滤
		file_path := ""
		if path == "/" {
			switch filepath.Ext(file_Name) {
			case ".txt", ".html", ".htm": // 目前只开放这种格式
				file_path = filepath.Join(file_Name)
			}

		} else {
			for _, dir := range self.server.Config.StaticDir {
				//如果第一个是静态文件夹名则选用主静态文件夹,反之使用模块
				// /static/js/base.js
				// /ModuleName/static/js/base.js
				dirs := strings.Split(path, "/")
				if strings.EqualFold(dirs[1], dir) {
					file_path = filepath.Join(req.URL.Path)
					break

				} else if strings.EqualFold(dirs[2], dir) { // 如果请求是 products/Static/js/base.js
					//Debug("lDirsD", lDirs, STATIC_DIR, string(os.PathSeparator))
					// 再次检查 Module Name 后必须是 /static 目录
					file_path = filepath.Join(
						MODULE_DIR, // c:\project\Modules
						req.URL.Path)
					break
				}
			}
		}

		// the path is not allow to visit
		if file_path != "" {
			// 当模块路径无该文件时，改为程序static文件夹
			if !utils.FileExists(file_path) {
				idx := strings.Index(file_path, STATIC_DIR)
				if idx != -1 {
					file_path = file_path[idx-1:]

				}
			}

			// 当程序文件夹无该文件时
			if utils.FileExists(file_path) { //TODO 缓存结果避免IO
				// need full path for ServeFile()
				file_path = filepath.Join(
					AppPath,
					file_path)

				// serve the file with full path
				nethttp.ServeFile(w, req, file_path)
				return
			}
		}
	}

	nethttp.NotFound(w, req)
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
		args     []reflect.Value //handler参数
		ctrl_val reflect.Value
		ctrl_typ reflect.Type
		parm     reflect.Type
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
					//log.Dbg("aaa", parm.Kind())
					if i == 0 && parm.Kind() == reflect.Struct { // 第一个 //第一个是方法的结构自己本身 例：(self TMiddleware) ProcessRequest（）的 self
						ctrl_typ = parm
						ctrl_val = self.objectPool.Get(parm)
						if ctrl_val.Kind() == reflect.Ptr {
							ctrl_val = ctrl_val.Elem()
						}
						// ctrl_val 由类生成实体值,必须指针转换而成才是Addressable  错误：lVal := reflect.Zero(aHandleType)
						args = append(args, ctrl_val) //插入该类型空值
						break
					}

					// TODO 优化判断  插入RPC 参数和返回
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

		CtrlValidable := ctrl_val.IsValid()
		if CtrlValidable {
			self.routeMiddleware("request", route, handler, ct, ctrl_val)
		}

		if !handler.IsDone() {
			// execute Handler or Panic Event
			self.safelyCall(ctrl.Func, args, route, handler, ct, ctrl_val) //传递参数给函数.<<<
		}

		if !handler.IsDone() && CtrlValidable {
			self.routeMiddleware("response", route, handler, ct, ctrl_val)
		}

		if CtrlValidable {
			self.objectPool.Put(ctrl_typ, ctrl_val)
		}
	}
}

func (self *TRouter) safelyCall(function reflect.Value, args []reflect.Value, route *TRoute, handler IHandler, ct *TController, ctrl reflect.Value) {
	defer func() {
		if err := recover(); err != nil {
			if self.server.Config.RecoverPanic { //是否绕过错误处理直接关闭程序
				// handle middleware
				self.routeMiddleware("panic", route, handler, ct, ctrl)

				// report error information
				log.Errf("r:%s err:%v", route.Path, err)
				for i := 1; ; i++ {
					_, file, line, ok := runtime.Caller(i)
					if !ok {
						break
					}
					log.Errf("line: %d file: %s", line, file)
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
	//self.routeMiddleware("disconnected", route, handler, ct, ctrl_val)
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
	p := req.URL.Path //获得的地址

	// # match route from tree
	route, params := self.tree.Match(req.Method, p)
	if route == nil {
		self.routeHttpStatic(req, w) // # serve as a static file link
		return
	}

	if self.show_route {
		self.server.logger.Info("[Path]%v [Route]%v", p, route.FilePath)
	}

	/*
		if route.isReverseProxy {
			self.routeProxy(route, params, req, w)

			return
		}
	*/

	// # init Handler
	handler := self.webHandlerPool.Get().(*TWebHandler)
	handler.reset(w, req, self, route)
	handler.setPathParams(params)

	self.callCtrl(route, &TController{webHandler: handler}, handler)

	// TODO 考虑废除
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
		// log.Dbg(STATIC_DIR, path.Join(utils.FilePathToPath(route.FilePath), STATIC_DIR))
		for _, dir := range self.server.Config.StaticDir {
			handler.templateVar[dir] = path.Join(utils.FilePathToPath(route.FilePath), dir)
		}
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
	res.Metadata["__rpc_error__"] = err.Error()
	return res, err
}

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
