package server

import (
	//	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"
	log "vectors/logger"
	"vectors/rpc/codec"
	//	"vectors/utils"
	"vectors/web"
)

// Can connect to RPC service using HTTP CONNECT to rpcPath.
var connected = "200 Connected to Go RPC"

type (
	TRouter struct {
		handlerMapMu sync.RWMutex
		handlerMap   map[string]*TModule
		readTimeout  time.Duration
		writeTimeout time.Duration

		msgPool    sync.Pool
		objectPool *web.TPool

		Server *TServer
		tree   *web.TTree
		lock   sync.RWMutex
	}
)

func NewRouter() *TRouter {
	router := &TRouter{}

	router.msgPool.New = func() interface{} {
		return func() interface{} {
			header := Header([12]byte{})
			header[0] = magicNumber

			return &TMessage{
				Header: &header,
			}
		}
	}
	return router
}

func (self *TRouter) RegisterModule(aMd web.IModule, build_path ...bool) {
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

	self.lock.Lock() //<-锁
	self.tree.Conbine(aMd.GetRoutes())
	///self.Routes = append(self.Routes, lRoutes...) // 注意要加[省略号] !!!暂时有重复合并问题
	//self.Routes = MergeMaps(self.Routes, m.Routes) // 合并两个Maps安全点
	self.lock.Unlock() //<-

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

func (self *TRouter) ServeTCP(msg net.Conn) {
	self.routeHandler(msg)
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
	self.ServeTCP(conn)
}

func (self *TRouter) safelyCall(function reflect.Value, args []reflect.Value, aActionValue reflect.Value) []reflect.Value {
	return function.Call(args)
}

func (self *TRouter) routeHandler(conn net.Conn) {
	req := self.msgPool.Get().(*TMessage) // request message

	// 获得请求参数
	err := req.Decode(conn)
	if err != nil {
		if err == io.EOF {
			log.Info("client has closed this connection: %s", conn.RemoteAddr().String())
		} else if strings.Contains(err.Error(), "use of closed network connection") {
			log.Info("rpc: connection %s is closed", conn.RemoteAddr().String())
		} else {
			log.Warn("rpc: failed to read request: %v", err)
		}
		return
	}

	if self.writeTimeout != 0 {
		conn.SetWriteDeadline(time.Now().Add(self.writeTimeout))
	}

	go func() {
		// 心跳包 直接返回
		if req.IsHeartbeat() {
			req.SetMessageType(Response)
			data := req.Encode()
			conn.Write(data)
			return
		}

		resMetadata := make(map[string]string)
		//newCtx := context.WithValue(context.WithValue(ctx, share.ReqMetaDataKey, req.Metadata),
		//	share.ResMetaDataKey, resMetadata)

		//		ctx := context.WithValue(context.Background(), RemoteConnContextKey, conn)

		//res, err := s.handleRequest(newCtx, req)
		//if err != nil {
		//	log.Warnf("rpcx: failed to handle request: %v", err)
		//}
		// 执行控制器
		var (
			coder      codec.ICodec
			args       []reflect.Value //handler参数
			replyv     []reflect.Value
			lActionVal reflect.Value
			//lActionTyp reflect.Type
			parm reflect.Type
			//		lIn           interface{}
			//CtrlValidable bool
		)
		serviceName := req.ServicePath
		//methodName := req.ServiceMethod

		// 克隆
		resraw := self.msgPool.Get().(*TMessage)
		res := req.Clone(resraw)
		self.msgPool.Put(resraw)

		res.SetMessageType(Response)
		// 匹配路由树
		route, _ := self.tree.Match("rpc", req.Path)
		if route == nil {
			err = errors.New("rpcx: can't match route " + serviceName)
			res, err = handleError(res, err)
			goto ret
		}

		// 获取支持的序列模式
		coder = codec.Codecs[req.SerializeType()]
		if coder == nil {
			err = fmt.Errorf("can not find codec for %d", req.SerializeType())
			res, err = handleError(res, err)
			goto ret
		}

		// 序列化
		//var argv = self.objectPool.Get(route.MainCtrl.ArgType)
		err = coder.Decode(req.Payload, route.MainCtrl.ArgType)
		if err != nil {
			res, err = handleError(res, err)
			goto ret
		}

		//replyv = self.objectPool.Get(route.MainCtrl.ReplyType)
		for _, ctrl := range route.Ctrls {
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
					if i == 0 && parm.Kind() == reflect.Struct { // 第一个 //第一个是方法的结构自己本身 例：(self TMiddleware) ProcessRequest（）的 self
						//lActionTyp = parm
						lActionVal = self.objectPool.Get(parm)
						if !lActionVal.IsValid() {
							lActionVal = reflect.New(parm).Elem() //由类生成实体值,必须指针转换而成才是Addressable  错误：lVal := reflect.Zero(aHandleType)
						}
						args = append(args, lActionVal) //插入该类型空值
						break
					}

					// STEP#:
					args = append(args, reflect.Zero(parm)) //插入该类型空值

				}
			}

			replyv = self.safelyCall(ctrl.Func, args, lActionVal) //传递参数给函数.<<<

			//s.Plugins.DoPreWriteResponse(newCtx, req)

		}
		if !req.IsOneway() {
			data, err := coder.Encode(replyv)
			//argsReplyPools.Put(mtype.ReplyType, replyv)
			if err != nil {
				res, err = handleError(res, err)
				goto ret
			}
			res.Payload = data
		}

	ret:
		// 组织完成非单程 必须返回的
		if !req.IsOneway() {
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

			data := res.Encode()
			conn.Write(data)
			//res.WriteTo(conn)
		}

		//s.Plugins.DoPostWriteResponse(newCtx, req, res, err)

		//protocol.FreeMsg(req)
		//protocol.FreeMsg(res)
	}()
}

func handleError(res *TMessage, err error) (*TMessage, error) {
	res.SetMessageStatusType(Error)
	if res.Metadata == nil {
		res.Metadata = make(map[string]string)
	}
	res.Metadata["__rpcx_error__"] = err.Error()
	return res, err
}
