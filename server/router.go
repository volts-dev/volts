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
	"vectors/rpc/codec"
	"vectors/rpc/protocol"
	"vectors/rpc/server/listener/http"
	"vectors/rpc/server/listener/rpc"

	"github.com/VectorsOrigin/utils"

	"github.com/VectorsOrigin/logger"
	log "github.com/VectorsOrigin/logger"
	"github.com/VectorsOrigin/template"
)

// Can connect to RPC service using HTTP CONNECT to rpcPath.
var connected = "200 Connected to RPC"

type (
	TRouter struct {
		sync.RWMutex
		handlerMapMu sync.RWMutex
		handlerMap   map[string]*TModule
		// TODO 全局变量. 需改进
		GVar map[string]interface{}
		// TODO 修改进其他包 或者独立出来
		Template *template.TTemplateSet

		readTimeout  time.Duration
		writeTimeout time.Duration
		show_route   bool

		//msgPool    sync.Pool
		objectPool  *TPool
		handlerPool sync.Pool
		respPool    sync.Pool

		Server     *TServer
		tree       *TTree
		middleware *TMiddlewareManager // 中间件

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
	}
	/*
		router.msgPool.New = func() interface{} {

			header := message.Header([12]byte{})
			header[0] = message.MagicNumber

			return &message.TMessage{
				Header: &header,
			}

		}
	*/
	return router
}

func (self *TRouter) init() {
	if self.Server.Config.PrintRouterTree {
		self.tree.PrintTrees()
	}

}

func (self *TRouter) RegisterModule(aMd IModule, build_path ...bool) {
	if aMd == nil {
		log.Warn("RegisterModule is nil")
		return
	}

	self.Lock() //<-锁
	self.tree.Conbine(aMd.GetRoutes())
	self.Unlock() //<-
}

// 注册中间件
func (self *TRouter) RegisterMiddleware(aMd ...IMiddleware) {
	for _, m := range aMd {
		lType := reflect.TypeOf(m)
		if lType.Kind() == reflect.Ptr {
			lType = lType.Elem()
		}
		lName := lType.String()
		self.middleware.Add(lName, m)
	}

}

// TODO:过滤 _ 的中间件
func (self *TRouter) routeBefore(route *TRoute, hd IHandler, ctrl reflect.Value) {
	// Action结构外的其他中间件
	var (
		IsFound         bool
		lField, lMethod reflect.Value
		lType           reflect.Type
		lNew            interface{}
		//		ml              IMiddleware
	)

	for _, key := range self.middleware.Names {
		// @:直接返回 放弃剩下的Handler
		if hd.Done() {
			break
		}

		ml := self.middleware.Get(key)
		IsFound = false
		//TODO 优化遍历
		for i := 0; i < ctrl.NumField(); i++ { // Action结构下的中间件
			lField = ctrl.Field(i) // 获得成员
			lType = lField.Type()

			if lType.Kind() == reflect.Ptr {
				lType = lType.Elem()
			}

			if lType.String() == key && ml != nil {
				if lField.IsNil() {
					lNew = cloneInterfacePtrFeild(ml) // 克隆
					lMVal := reflect.ValueOf(lNew)    // or reflect.ValueOf(lMiddleware).Convert(lField.Type())
					if lField.Kind() == lMVal.Kind() {
						lField.Set(lMVal) // 通过
					}
				}

				lMethod = lField.MethodByName("Request")
				//Warn("routeBefore", key, lMethod.IsValid())
				if lMethod.IsValid() {
					lMethod.Call([]reflect.Value{ctrl, reflect.ValueOf(route)}) //执行方法
				}
			}
			// STEP:结束循环
			IsFound = true
			break
		}

		// 更新非控制器中的中间件
		if !IsFound {
			//Warn(" routeBefore not IsFound", key, ctrl.Interface(), hd)
			ml.Request(ctrl.Interface(), route)
		}
	}
}

func (self *TRouter) routeAfter(route *TRoute, hd IHandler, ctrl reflect.Value) {
	var (
		lField, lMethod reflect.Value
		lType           reflect.Type
		lNew            interface{}
	)
	for key, ml := range self.middleware.middlewares {
		for i := 0; i < ctrl.NumField(); i++ { // Action结构下的中间件
			lField = ctrl.Field(i) // 获得成员
			lType = lField.Type()

			if lType.Kind() == reflect.Ptr {
				lType = lType.Elem()
			}

			//self.Server.Logger.DbgLn("Name %s %s", key, lType.Name(), lField.Interface(), lField.Kind(), lField.String())

			if lType.String() == key {
				//Warn("lField.IsValid(),lField.IsNil()", lField.IsValid(), lField.IsNil())
				//if lField.IsValid() { // 存在该Filed
				//	过滤继承的结构体
				//	type TAction struct {
				//		TEvent
				//	}
				if lField.Kind() != reflect.Struct && lField.IsNil() {
					//Warn("!ctrl.IsValid()", lrActionVal)
					lNew = cloneInterfacePtrFeild(ml)                    // 克隆
					lNew.(IMiddleware).Response(ctrl.Interface(), route) // 首先获得基本数据
					lMVal := reflect.ValueOf(lNew)                       // or reflect.ValueOf(lMiddleware).Convert(lField.Type())

					if lField.Kind() == lMVal.Kind() {
						lField.Set(lMVal) // 通过
					}
				} else {
					// 尝试获取方法
					lMethod = lField.MethodByName("Response")
					if lMethod.IsValid() {
						lMethod.Call([]reflect.Value{ctrl, reflect.ValueOf(route)}) //执行方法
					}
				}
				//}

				// STEP:结束循环
				break
			} else {
				//Warn(" routeBefore", key, ctrl.Interface(), hd)
				ml.Response(ctrl.Interface(), route)
			}
		}
	}
}

func (self *TRouter) routePanic(route *TRoute, hd IHandler, ctrl reflect.Value) {
	if ctrl.IsValid() {
		lNameLst := make(map[string]bool)
		// @@@@@@@@@@有待优化 可以缓存For结果
		for i := 0; i < ctrl.NumField(); i++ { // Action结构下的中间件
			lField := ctrl.Field(i) // 获得成员
			lType := lField.Type()

			// 过滤继承结构的中间件
			if lField.Kind() == reflect.Struct {
				continue
			}

			if lType.Kind() == reflect.Ptr {
				lType = lType.Elem()
			}
			lFieldName := lType.Name() + lType.String()
			if self.middleware.Contain(lFieldName) {
				lNameLst[lFieldName] = true
				m := lField.MethodByName("Panic")
				if m.IsValid() {
					lHdValue := reflect.ValueOf(hd)
					//self.Logger.Info("", m, lHdValue, ctrl)
					m.Call([]reflect.Value{ctrl, lHdValue}) //执行方法
				}
			}
		}

		// 重复斌执行上面 遗漏的
		for key, ml := range self.middleware.middlewares { // Action结构下的中间件
			if !lNameLst[key] && ml != nil {
				ml.Panic(ctrl.Interface(), route)
			}
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
			log.Info("rpc hijacking ", req.RemoteAddr, ": ", err.Error())
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
			log.Info("rpc Read ", err.Error())
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
func (self *TRouter) callCtrl(route *TRoute, handler IHandler) {
	var (
		args          []reflect.Value //handler参数
		lActionVal    reflect.Value
		lActionTyp    reflect.Type
		lParm         reflect.Type
		CtrlValidable bool
	)

	// TODO:将所有需要执行的Handler 存疑列表或者树-Node保存函数和参数
	//logger.Dbg("lParm %s %d:%d %p %p", handler.TemplateSrc, lRoute.Action, lRoute.MainCtrl, len(lRoute.Ctrls), lRoute.Ctrls)
	for _, ctrl := range route.Ctrls {
		//handler.CtrlIndex = index //index
		// STEP#: 获取<Ctrl.Func()>方法的参数
		for i := 0; i < ctrl.FuncType.NumIn(); i++ {
			lParm = ctrl.FuncType.In(i) // 获得参数

			//self.Logger.DbgLn("lParm%d:", i, lParm, lParm.Name())
			switch lParm { //arg0.Elem() { //获得Handler的第一个参数类型.
			case handler.TypeModel(): // if is a pointer of TWebHandler
				{
					//args = append(args, reflect.ValueOf(handler)) // 这里将传递本函数先前创建的handle 给请求函数
					args = append(args, handler.ValueModel()) // 这里将传递本函数先前创建的handle 给请求函数
				}
			default:
				{
					//
					//Trace("lParm->default")
					if i == 0 && lParm.Kind() == reflect.Struct { // 第一个 //第一个是方法的结构自己本身 例：(self TMiddleware) ProcessRequest（）的 self
						lActionTyp = lParm
						lActionVal = self.objectPool.Get(lParm)
						if !lActionVal.IsValid() {
							lActionVal = reflect.New(lParm).Elem() //由类生成实体值,必须指针转换而成才是Addressable  错误：lVal := reflect.Zero(aHandleType)
						}
						args = append(args, lActionVal) //插入该类型空值
						break
					}

					// STEP:如果是参数是 http.ResponseWriter 值
					if strings.EqualFold(lParm.String(), "http.ResponseWriter") { // Response 类
						//args = append(args, reflect.ValueOf(w.ResponseWriter))
						args = append(args, reflect.ValueOf(handler.Response()))
						break
					}

					// STEP:如果是参数是 http.Request 值
					if lParm == reflect.TypeOf(handler.Request()) { // request 指针
						args = append(args, reflect.ValueOf(handler.Request())) //TODO (同上简化reflect.ValueOf）
						break
					}

					// STEP#:
					args = append(args, reflect.Zero(lParm)) //插入该类型空值

				}
			}
		}

		CtrlValidable = lActionVal.IsValid()
		if CtrlValidable {
			//self.Logger.Info("routeBefore")
			self.routeBefore(route, handler, lActionVal)
		}
		//logger.Infof("safelyCall %v ,%v", handler.Response.Written(), args)
		if !handler.Done() {
			//self.Logger.Info("safelyCall")
			// -- execute Handler or Panic Event
			self.safelyCall(ctrl.Func, args, route, handler, lActionVal) //传递参数给函数.<<<
		}

		if !handler.Done() && CtrlValidable {
			// # after route
			self.routeAfter(route, handler, lActionVal)
		}

		if CtrlValidable {
			self.objectPool.Put(lActionTyp, lActionVal)
		}
	}
}

func (self *TRouter) safelyCall(function reflect.Value, args []reflect.Value, route *TRoute, hd IHandler, ctrl reflect.Value) (reflect.Value, error) {
	// 错误处理
	defer func() {
		if err := recover(); err != nil {
			if self.Server.Config.RecoverPanic { //是否绕过错误处理直接关闭程序
				self.routePanic(route, hd, ctrl)

				for i := 1; ; i++ {
					_, file, line, ok := runtime.Caller(i)
					if !ok {
						break
					}
					logger.Err(file, line)
				}
			} else {
				logger.Panic("", err)
			}
		}
	}()

	// TODO 优化速度GO2
	// Invoke the method, providing a new value for the reply.
	returnValues := function.Call(args)
	// The return value for the method is an error.
	errInter := returnValues[0].Interface()
	if errInter != nil {
		return reflect.ValueOf(nil), errInter.(error)
	}

	return args[2], nil
}

func (self *TRouter) routeRpc(w rpc.Response, req *rpc.Request) (*protocol.TMessage, error) {
	// 心跳包 直接返回
	if req.Message.IsHeartbeat() {
		//data := req.Message.Encode()
		//w.Write(data)
		//TODO 补全状态吗
		return nil, nil
	}

	var (
		coder codec.ICodec
		//		args       []reflect.Value //handler参数
		replyv reflect.Value
		//	lActionVal reflect.Value
		//  lActionTyp reflect.Type
		//	parm reflect.Type
		//		lIn           interface{}
		//CtrlValidable bool
		err error
	)

	resMetadata := make(map[string]string)
	//newCtx := context.WithValue(context.WithValue(ctx, share.ReqMetaDataKey, req.Metadata),
	//	share.ResMetaDataKey, resMetadata)

	//		ctx := context.WithValue(context.Background(), RemoteConnContextKey, conn)
	msg := req.Message
	serviceName := msg.ServicePath
	//methodName := msg.ServiceMethod
	//log.Dbg("3333", msg.SerializeType())

	// 克隆
	resraw := protocol.GetMessageFromPool()
	res := msg.Clone(resraw)
	//protocol.PutMessageToPool(resraw)
	//log.Dbg("handleRequest", msg.SerializeType(), res.SerializeType())
	res.SetMessageType(protocol.Response)
	// 匹配路由树
	log.Dbg("handleRequest", msg.Path, msg.ServicePath+"."+msg.ServiceMethod)
	//route, _ := self.tree.Match("HEAD", msg.ServicePath+"."+msg.ServiceMethod)
	route, _ := self.tree.Match("CONNECT", msg.Path)
	if route == nil {
		err = errors.New("rpc: can't match route " + serviceName)
		return handleError(res, err)
	}

	// 获取支持的序列模式
	coder = codec.Codecs[msg.SerializeType()]
	if coder == nil {
		err = fmt.Errorf("can not find codec for %d", msg.SerializeType())
		return handleError(res, err)
	}

	// 获取RPC参数
	log.Dbg("handleRequest", msg.SerializeType())
	// 序列化
	var argv = self.objectPool.Get(route.MainCtrl.ArgType)
	err = coder.Decode(msg.Payload, argv.Interface())
	if err != nil {
		return handleError(res, err)
	}
	log.Dbg("handleRequest", argv)
	replyv = self.objectPool.Get(route.MainCtrl.ReplyType)
	log.Dbg("ctrl", argv, replyv)
	//args = append(args, reflect.Zero(typeOfContext))
	//args = append(args, argv)
	//args = append(args, replnjb.xv..gpotvyyv)

	// 执行控制器
	self.callCtrl(route, nil)
	/*
		res, err := self.handleRequest(req.Message)
		if err != nil {
			log.Warnf("rpc: failed to handle request: %v", err)
		}
	*/

	// 返回数据
	log.Dbg("handleRequest", msg.IsOneway())
	if !msg.IsOneway() {
		log.Dbg("!IsOneway")
		// 序列化数据
		data, err := coder.Encode(replyv.Interface())
		//argsReplyPools.Put(mtype.ReplyType, replyv)
		if err != nil {
			return handleError(res, err)

		}
		log.Dbg("data", replyv.Interface(), string(data))
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
		log.Dbg("aa", string(res.Payload))
		cnt, err = w.Write(res.Payload)
		if err != nil {
			log.Dbg(err.Error())
		}
		log.Dbg("aa", cnt, string(res.Payload))
	}

	return nil, nil
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
		logger.Info("[Path]%v [Route]%v", lPath, lRoute.FilePath)
	}

	//opy(lParam, Param)
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
	handler := self.handlerPool.Get().(*TWebHandler)
	handler.connect(w, req, self, lRoute)

	for _, param := range lParam {
		handler.setPathParams(param.Name, param.Value)
		//self.Logger.DbgLn("lParam", param.Name, param.Value)
	}

	self.callCtrl(lRoute, handler)

	if handler.finalCall.IsValid() {
		if f, ok := handler.finalCall.Interface().(func(*TWebHandler)); ok {
			//f([]reflect.Value{reflect.ValueOf(lHandler)})
			f(handler)
			//			Trace("Handler Final Call")
		}
	}

	//##################
	//设置某些默认头
	//设置默认的 content-type
	//TODO 由Tree完成
	//tm := time.Now().UTC()
	lHandler.SetHeader(true, "Engine", "vectors web") //取当前时间
	//lHandler.SetHeader(true, "Date", WebTime(tm)) //
	//lHandler.SetHeader(true, "Content-Type", "text/html; charset=utf-8")
	if lHandler.TemplateSrc != "" {
		//添加[static]静态文件路径
		logger.Dbg(STATIC_DIR, path.Join(utils.FilePathToPath(lRoute.FilePath), STATIC_DIR))
		//	self.AddVar(STATIC_DIR, path.Join(utils.FilePathToPath(lRoute.FilePath), STATIC_DIR)) //添加[static]静态文件路径
		lHandler.RenderArgs[STATIC_DIR] = path.Join(utils.FilePathToPath(lRoute.FilePath), STATIC_DIR)
	}

	// 结束Route并返回内容
	lHandler.Apply()

	self.handlerPool.Put(lHandler) // Pool 回收Handler
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
func cloneInterfacePtrFeild(s interface{}) interface{} {
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
	return lrVal.Interface()
}
