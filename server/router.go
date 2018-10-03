package server

import (

	//	"context"

	"errors"
	"fmt"
	"io"

	//	"net"
	"net/http"
	"reflect"
	"sync"
	"time"
	"vectors/rpc/codec"
	"vectors/rpc/protocol"
	"vectors/rpc/server/listener/rpc"

	log "github.com/VectorsOrigin/logger"

	//	"vectors/utils"
	"vectors/web"
)

// Can connect to RPC service using HTTP CONNECT to rpcPath.
var connected = "200 Connected to RPC"

type (
	TRouter struct {
		sync.RWMutex
		handlerMapMu sync.RWMutex
		handlerMap   map[string]*TModule
		readTimeout  time.Duration
		writeTimeout time.Duration

		//msgPool    sync.Pool
		objectPool *web.TPool

		Server *TServer
		tree   *web.TTree
	}
)

func NewRouter() *TRouter {
	tree := web.NewRouteTree()
	tree.IgnoreCase = true
	tree.DelimitChar = '.' // 修改为xxx.xxx

	router := &TRouter{
		tree:       tree,
		handlerMap: make(map[string]*TModule),
		objectPool: web.NewPool(),
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
	self.tree.PrintTrees()

}

func (self *TRouter) RegisterModule(aMd IModule, build_path ...bool) {
	if aMd == nil {
		log.Warn("RegisterModule is nil")
		return
	}

	// 执行注册器接口
	//if a, ok := aMd.(IModuleRegister); ok {
	//	a.Register()
	//}

	///lRoutes := aMd.GetRoutes()
	//	lModuleFilePath := utils.Trim(aMd.GetFilePath())

	//self.Logger.("RegisterModules:", reflect.TypeOf(aMd))

	self.Lock() //<-锁
	self.tree.Conbine(aMd.GetRoutes())
	///self.Routes = append(self.Routes, lRoutes...) // 注意要加[省略号] !!!暂时有重复合并问题
	//self.Routes = MergeMaps(self.Routes, m.Routes) // 合并两个Maps安全点
	self.Unlock() //<-

	/*
		//#创建文件夹
		//os.Mkdir("./modules/aa", 0700) //>>>>>>>>>>

		// The Path must be not blank.
		// <待优化静态路径管理>必须不是空白路径才能组合正确
		if len(build_path) > 0 && build_path[0] && len(lModuleFilePath) > 0 {
			lModuleFilePath := filepath.Join(self.Server.Config.ModulesDir, lModuleFilePath)
			err := os.Mkdir(lModuleFilePath, 0700)
			if err != nil {
				os.Mkdir(filepath.Join(lModuleFilePath, self.Server.Config.StaticDir), 0700)
				os.Mkdir(filepath.Join(lModuleFilePath, self.Server.Config.TemplatesDir), 0700)
			}
		}
	*/
}

func (self *TRouter) ServeTCP(w rpc.Response, req *rpc.Request) {
	self.routeHandler(w, req)
}

func (self *TRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != "CONNECT" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusMethodNotAllowed)
		io.WriteString(w, "405 must CONNECT\n")
		return
	}

	conn, _, err := w.(http.Hijacker).Hijack()
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
}

func (self *TRouter) safelyCall(function reflect.Value, args []reflect.Value, aActionValue reflect.Value) (reflect.Value, error) {
	log.Dbg("call")
	// Invoke the method, providing a new value for the reply.
	returnValues := function.Call(args)
	// The return value for the method is an error.
	errInter := returnValues[0].Interface()
	if errInter != nil {
		return reflect.ValueOf(nil), errInter.(error)
	}

	return args[2], nil
}

// 执行控制器
func (self *TRouter) handleRequest(msg *protocol.TMessage) (*protocol.TMessage, error) {

	var (
		coder      codec.ICodec
		args       []reflect.Value //handler参数
		replyv     reflect.Value
		lActionVal reflect.Value
		//lActionTyp reflect.Type
		parm reflect.Type
		//		lIn           interface{}
		//CtrlValidable bool
		err error
	)
	//msg := req.Message
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
	args = append(args, reflect.Zero(typeOfContext))
	args = append(args, argv)
	args = append(args, replyv)
	for _, ctrl := range route.Ctrls {
		log.Dbg("ctrl", ctrl, ctrl.FuncType.NumIn())
		// 获取参数值
		for i := 0; i < ctrl.FuncType.NumIn(); i++ {
			parm = ctrl.FuncType.In(i) // 获得参数

			//self.Logger.DbgLn("lParm%d:", i, lParm, lParm.Name())
			switch parm { //arg0.Elem() { //获得Handler的第一个参数类型.
			/*case reflect.TypeOf(lHandler): // if is a pointer of THandler
			{
				//args = append(args, reflect.ValueOf(lHandler)) // 这里将传递本函数先前创建的handle 给请求函数
				args = append(args, lHandler.val) // 这里将传递本函数先前创建的handle 给请求函数
			}
			*/
			default:
				// 处理结构体指针
				//Trace("lParm->default")
				log.Dbg("default", parm.Kind(), parm.String())
				if i == 0 && parm.Kind() == reflect.Struct { // 第一个 //第一个是方法的结构自己本身 例：(self TMiddleware) ProcessRequest（）的 self
					//lActionTyp = parm
					log.Dbg("default")
					lActionVal = self.objectPool.Get(parm)
					if !lActionVal.IsValid() {
						lActionVal = reflect.New(parm).Elem() //由类生成实体值,必须指针转换而成才是Addressable  错误：lVal := reflect.Zero(aHandleType)
					}
					args = append(args, lActionVal) //插入该类型空值
					break
				}

				// STEP#:
				//args = append(args, reflect.Zero(parm)) //插入该类型空值

			}
		}

		replyv, err = self.safelyCall(ctrl.Func, args, lActionVal) //传递参数给函数.<<<
		if err != nil {
			log.Errf("", err.Error())
		}
		//log.Dbg("adf", *replyv.Interface().(*Reply))
		//s.Plugins.DoPreWriteResponse(newCtx, msg)

	}
	log.Dbg("handleRequest", msg.IsOneway())
	if !msg.IsOneway() {
		data, err := coder.Encode(replyv.Interface())
		//argsReplyPools.Put(mtype.ReplyType, replyv)
		if err != nil {
			return handleError(res, err)

		}
		log.Dbg("data", replyv.Interface(), string(data))
		res.Payload = data
	}

	return res, nil
}

func (self *TRouter) routeHandler(w rpc.Response, req *rpc.Request) {
	// 心跳包 直接返回
	if req.Message.IsHeartbeat() {
		//data := req.Message.Encode()
		//w.Write(data)
		//TODO 补全状态吗
		return
	}

	resMetadata := make(map[string]string)
	//newCtx := context.WithValue(context.WithValue(ctx, share.ReqMetaDataKey, req.Metadata),
	//	share.ResMetaDataKey, resMetadata)

	//		ctx := context.WithValue(context.Background(), RemoteConnContextKey, conn)

	res, err := self.handleRequest(req.Message)
	if err != nil {
		log.Warnf("rpc: failed to handle request: %v", err)
	}
	log.Dbg("ttttt", *req)
	// 组织完成非单程 必须返回的
	if !req.Message.IsOneway() {
		log.Dbg("!IsOneway")
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
		//log.Dbg("!IsOneway", res)
		//data := res.Encode()
		//log.Dbg("!IsOneway", string(res.Payload), string(data), res.Metadata)
		//time.Sleep(5 * time.Second)
		var cnt int
		log.Dbg("aa", string(res.Payload))
		cnt, err = w.Write(res.Payload)
		if err != nil {
			log.Dbg(err.Error())
		}
		log.Dbg("aa", cnt, string(res.Payload))

	}
}

func handleError(res *protocol.TMessage, err error) (*protocol.TMessage, error) {
	res.SetMessageStatusType(protocol.Error)
	if res.Metadata == nil {
		res.Metadata = make(map[string]string)
	}
	res.Metadata["__rpcx_error__"] = err.Error()
	return res, err
}
