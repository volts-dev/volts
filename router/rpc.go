package router

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/transport"
	"github.com/volts-dev/volts/util/body"
)

var (
	RpcContextType = reflect.TypeOf(&TRpcContext{}) // must be a pointer
	RpcContext     = "RpcContext"                   // 标识用于判断String()
)

type (
	// 代表一个控制集
	TRpcContext struct {
		logger.ILogger
		context      context.Context
		response     *transport.RpcResponse //http.ResponseWriter
		request      *transport.RpcRequest  //
		router       *TRouter
		data         *TParamsSet // 数据缓存在各个Controler间调用
		route        route       //执行本次Handle的Route
		inited       bool
		handlerIndex int
		handler      *handler
		name         string        // name of service
		__rcvr       reflect.Value // receiver of methods for the service
		val          reflect.Value
		typ          reflect.Type // type of the receiver
		//method   map[string]*methodType   // registered methods
		//function map[string]*functionType // registered functions
		isDone bool // -- 已经提交过

		argv   reflect.Value
		replyv reflect.Value
	}
)

func handleError(res *transport.Message, err error) (*transport.Message, error) {
	res.SetMessageStatusType(transport.Error)
	if res.Header == nil {
		res.Header = make(map[string]string)
	}
	res.Header["__rpc_error__"] = err.Error()
	return res, err
}

func NewRpcHandler(router *TRouter) *TRpcContext {
	handler := &TRpcContext{
		ILogger: log,
		router:  router,
	}
	handler.val = reflect.ValueOf(handler)
	handler.typ = handler.val.Type()
	return handler
}
func (self *TRpcContext) Router() IRouter {
	return self.router
}
func (self *TRpcContext) Request() *transport.RpcRequest {
	return self.request
}

func (self *TRpcContext) Response() *transport.RpcResponse {
	return self.response
}

func (self *TRpcContext) Route() route {
	return self.route
}

func (self *TRpcContext) setControllerIndex(num int) {
	self.handlerIndex = num
}

func (self *TRpcContext) HandlerIndex() int {
	return self.handlerIndex
}

func (self *TRpcContext) Handler(index ...int) *handler {
	if index != nil {
		return self.route.handlers[index[0]]
	}
	return self.route.handlers[self.handlerIndex]
}

func (self *TRpcContext) Context() context.Context {
	return self.context
}

func (self *TRpcContext) IsDone() bool {
	return self.isDone
}

// the reflect model of Value
func (self *TRpcContext) ValueModel() reflect.Value {
	return self.val
}

// the reflect model of Type
func (self *TRpcContext) TypeModel() reflect.Type {
	return self.typ
}

func (self *TRpcContext) reset(rw *transport.RpcResponse, req *transport.RpcRequest, Router IRouter, Route *route) {
	self.request = req
	self.response = rw
	self.data = nil // 清空
}

func (self *TRpcContext) setData(v interface{}) {

}

func (self *TRpcContext) String() string {
	return RpcContext
}

func (self *TRpcContext) Body() *body.TBody {
	return self.request.Body()
}

func (self *TRpcContext) Write(data []byte) (int, error) {
	return self.response.Write(data)
}

func (self *TRpcContext) WriteStream(data interface{}) error {
	return self.response.WriteStream(data)
}

func (self *TRpcContext) RespondByJson(data interface{}) {
	js, err := json.Marshal(data)
	if err != nil {
		self.response.Write([]byte(err.Error()))
		return
	}

	self.response.Write(js)
}
func (self *TRpcContext) Next() {
	self.handler.Invoke(self)
}

func (self *TRpcContext) Data() *TParamsSet {
	if self.data == nil {
		self.data = NewParamsSet(self)
	}

	return self.data
}

func (self *TRpcContext) setHandler(h *handler) {
	self.handler = h
}

func (self *TRpcContext) Abort(body string) {
	// TODO
	self.isDone = true
}
