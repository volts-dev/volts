package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"github.com/volts-dev/logger"
	"github.com/volts-dev/template"
	"github.com/volts-dev/utils"
	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/transport"
)

type (
	handler interface {
		// pravite
		//reset(rw IResponse, req IRequest, Router *TRouter, Route *TRoute)
		//setData(v interface{}) // TODO 修改API名称  设置response数据

		// public
		//Request() IRequest
		//Response() IResponse
		ValueModel() reflect.Value //
		TypeModel() reflect.Type
		IsDone() bool //response data is done
	}

	// router represents an RPC router.
	router struct {
		name       string
		server     *server
		tree       *TTree
		middleware *TMiddlewareManager // 中间件
		template   *template.TTemplateSet
		show_route bool
		//msgPool    sync.Pool
		objectPool     *TPool
		webHandlerPool sync.Pool
		rpcHandlerPool sync.Pool
		respPool       sync.Pool
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

func newRouter() *router {
	return &router{}
}

func (self *router) String() string {
	return "volts-router"
}

func (self *router) Handler() interface{} {
	return self
}

func (self *router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == "CONNECT" { // serve as a raw network server
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			logger.Infof("rpc hijacking %v:%v", r.RemoteAddr, ": ", err.Error())
		}
		io.WriteString(conn, "HTTP/1.0 200 Connected to RPC\n\n")
		/*
			s.mu.Lock()
			s.activeConn[conn] = struct{}{}
			s.mu.Unlock()
		*/

		msg, err := transport.ReadMessage(conn)
		if err != nil {
			logger.Info("rpc Read ", err.Error())
		}

		ctx := context.Background()
		req := &transport.RpcRequest{
			Message: msg,
			Context: ctx,
		}

		self.ServeRPC(w, req)
	} else { // serve as a web server
		// Pool 提供TResponseWriter
		rsp := self.respPool.Get().(*httpResponse)
		rsp.Connect(w.(http.ResponseWriter))
		defer func() {
			// Pool 回收TResponseWriter
			rsp.rsp = nil
			self.respPool.Put(rsp)
		}()
		path := r.URL.Path //获得的地址

		// # match route from tree
		route, params := self.tree.Match(r.Method, path)
		if route == nil {
			self.routeHttpStatic(w, r) // # serve as a static file link
			return
		}

		if self.show_route {
			logger.Infof("[Path]%v [Route]%v", path, route.FilePath)
		}

		/*
			if route.isReverseProxy {
				self.routeProxy(route, params, req, w)

				return
			}
		*/

		// # init Handler
		handler := self.webHandlerPool.Get().(*HttpHandler)
		handler.reset(w, r, self, route)
		handler.setPathParams(params)

		self.callCtrl(route, handler)

		//##################
		//设置某些默认头
		//设置默认的 content-type
		//TODO 由Tree完成
		//tm := time.Now().UTC()
		handler.SetHeader(true, "Engine", "Volts") //取当前时间
		/*
			if handler.TemplateSrc != "" {

				//添加[static]静态文件路径
				// log.Dbg(STATIC_DIR, path.Join(utils.FilePathToPath(route.FilePath), STATIC_DIR))
				for _, dir := range self.server.Config.StaticDir {
					handler.templateVar[dir] = path.Join(utils.FilePathToPath(route.FilePath), dir)
				}
			}*/

		// 结束Route并返回内容
		handler.Apply()
		self.webHandlerPool.Put(handler) // Pool 回收Handler
	}
}

func (self *router) ServeRPC(w transport.IResponse, r *transport.RpcRequest) {
	// 心跳包 直接返回
	if r.Message.IsHeartbeat() {
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
	msg := r.Message // return the packet struct
	serviceName := msg.Header["ServicePath"]
	//methodName := msg.ServiceMethod

	// TODO 无需克隆
	resraw := transport.GetMessageFromPool()
	res := msg.CloneTo(resraw)

	//protocol.PutMessageToPool(resraw)
	res.SetMessageType(transport.MT_RESPONSE)
	// 匹配路由树
	//route, _ := self.tree.Match("HEAD", msg.ServicePath+"."+msg.ServiceMethod)
	var coder codec.ICodec
	var handler *RpcHandler
	route, _ := self.tree.Match("CONNECT", msg.Path)
	if route == nil {
		err = errors.New("rpc: can't match route " + serviceName)
		handleError(res, err)
	} else {
		// 获取支持的序列模式
		coder = codec.IdentifyCodec(msg.SerializeType())
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
				handler = self.rpcHandlerPool.Get().(*RpcHandler)
				handler.reset(w, r, self, route)
				handler.argv = argv
				handler.replyv = self.objectPool.Get(route.MainCtrl.ReplyType)

				// 执行控制器
				self.callCtrl(route, handler)
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
			meta := res.Header
			if meta == nil {
				res.Header = resMetadata
			} else {
				for k, v := range resMetadata {
					meta[k] = v
				}
			}
		}

		var cnt int
		cnt, err = w.Write(res.Payload)
		if err != nil {
			logger.Dbg(err.Error())
		}
		logger.Dbg("aa", cnt, string(res.Payload))
	}

	return
}

// TODO 有待优化
// 执行静态文件路由
func (self *router) routeHttpStatic(w http.ResponseWriter, req *http.Request) {
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
			for _, dir := range self.server.config.StaticDir {
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
				http.ServeFile(w, req, file_path)
				return
			}
		}
	}

	http.NotFound(w, req)
	return
}

// the order is according by controller for modular register middleware.
// TODO 优化遍历 缓存中间件列表
// TODO 优化 route the midware request,response,panic
func (self *router) routeMiddleware(method string, route *TRoute, handler handler) {
	var (
		mid_val, mid_ptr_val reflect.Value
		mid_typ              reflect.Type
		mid_name             string // name of middleware
		controller           reflect.Value
	)

	// [指针值]转为[结构值]
	if ctrl.Kind() == reflect.Ptr {
		controller = ctrl.Elem()
	}

	// the minddleware list from the controller
	name_lst := make(map[string]bool)            // TODO　不用MAP list of midware found it ctrl
	for i := 0; i < controller.NumField(); i++ { // middlewares under controller
		// @:直接返回 放弃剩下的Handler
		if handler.IsDone() {
			name_lst = nil // not report
			break          // igonre the following any controls
		}

		mid_val = controller.Field(i) // get the middleware value
		mid_typ = mid_val.Type()      // get the middleware type

		if mid_typ.Kind() == reflect.Ptr {
			mid_typ = mid_typ.Elem()
		}

		// get the name of middleware from the Type or Name()
		mid_name = mid_typ.String()
		if mid_val.Kind() == reflect.Ptr {
			if m, ok := mid_val.Interface().(IMiddlewareName); ok {
				mid_name = m.Name()
			}
		} else if mid_val.Kind() == reflect.Struct {
			if m, ok := ctrl.Interface().(IMiddlewareName); ok {
				mid_name = m.Name()
			}
		}

		ml := self.middleware.Get(mid_name)
		if ml == nil {
			// normall only struct and pointer could be a middleware
			if mid_val.Kind() == reflect.Struct || mid_val.Kind() == reflect.Ptr {
				name_lst[mid_name] = false
			}
		} else {
			name_lst[mid_name] = true

			if mid_val.Kind() == reflect.Ptr {
				/***	!过滤指针中间件!
					type Controller struct {
						Session *TSession
					}
				***/
				// all middleware are nil at first time on the controller
				if mid_val.IsNil() {
					mid_ptr_val = cloneInterfacePtrFeild(ml) // TODO 优化克隆

					// set back the middleware pointer to the controller
					if mid_val.Kind() == mid_ptr_val.Kind() {
						mid_val.Set(mid_ptr_val) // TODO Warm: Field must exportable
					}
				}
			} else if mid_val.Kind() == reflect.Struct {
				mid_val = ctrl
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

	// report the name of midware which on controller but not register in the server
	for name, found := range name_lst {
		if !found {
			logger.Errf("%v isn't be register in controller %v", name, ctrl.String())
		}
	}
}

func (self *router) callCtrl(route *TRoute, ctx handler) {
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
		if ctx.IsDone() {
			break
		}

		//handler.ControllerIndex = index //index
		// STEP#: 获取<Ctrl.Func()>方法的参数
		for i := 0; i < ctrl.FuncType.NumIn(); i++ {
			parm = ctrl.FuncType.In(i) // 获得参数

			//log.Dbg("aaa", parm.String(), handler.TypeModel().String())
			switch parm { //arg0.Elem() { //获得Handler的第一个参数类型.
			case ctx.TypeModel(): // TODO 优化调用 // if is a pointer of TWebHandler
				{
					args = append(args, ctx.ValueModel()) // 这里将传递本函数先前创建的handle 给请求函数
				}
			default:
				{
					//
					//log.Dbg("aaa", parm.Kind())
					if i == 0 && parm.Kind() == reflect.Struct { // 第一个 //第一个是方法的结构自己本身 例：(self TMiddleware) ProcessRequest（）的 self
						ctrl_typ = parm
						ctrl_val = self.objectPool.Get(parm)

						// ctrl_val 由类生成实体值,必须指针转换而成才是Addressable  错误：lVal := reflect.Zero(aHandleType)
						if ctrl_val.Kind() == reflect.Ptr {
							args = append(args, ctrl_val.Elem()) //插入该类型空值
						}
						break
					}
					// by default append a zero value
					args = append(args, reflect.Zero(parm)) //插入该类型空值
				}
			}
		}

		CtrlValidable := ctrl_val.IsValid()
		if CtrlValidable {
			self.routeMiddleware("request", route, ctx, ctrl_val)
		}

		if !ctx.IsDone() {
			// execute Handler or Panic Event
			self.safelyCall(ctrl.Func, args, route, ctx, ctrl_val) //传递参数给函数.<<<
		}

		if !ctx.IsDone() && CtrlValidable {
			self.routeMiddleware("response", route, ctx, ctrl_val)
		}

		if CtrlValidable {
			self.objectPool.Put(ctrl_typ, ctrl_val)
		}
	}
}

func (self *router) safelyCall(function reflect.Value, args []reflect.Value, route *TRoute, handler handler, ctrl reflect.Value) {
	defer func() {
		if err := recover(); err != nil {
			/*	if self.server.Config.RecoverPanic { //是否绕过错误处理直接关闭程序
					// handle middleware
					self.routeMiddleware("panic", route, handler, ctrl)

					// report error information
					logger.Errf("r:%s err:%v", route.Path, err)
					for i := 1; ; i++ {
						_, file, line, ok := runtime.Caller(i)
						if !ok {
							break
						}
						logger.Errf("file: %s %d", file, line)
					}
				} else {
					panic(err)
				}
			*/
		}
	}()

	// TODO 优化速度GO2
	// Invoke the method, providing a new value for the reply.
	function.Call(args)
}
