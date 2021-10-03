package router

import (
	"context"
	"reflect"

	log "github.com/volts-dev/logger"
	"github.com/volts-dev/volts/transport"
)

var (
	RpcHandlerType = reflect.TypeOf(TRpcContext{})
)

type (
	// 代表一个控制集
	TRpcContext struct {
		log.ILogger
		context      context.Context
		response     transport.IResponse   //http.ResponseWriter
		request      *transport.RpcRequest //
		Router       *router
		route        route //执行本次Handle的Route
		inited       bool
		handlerIndex int
		name         string        // name of service
		__rcvr       reflect.Value // receiver of methods for the service
		val          reflect.Value
		typ          reflect.Type // type of the receiver

		//method   map[string]*methodType   // registered methods
		//function map[string]*functionType // registered functions
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

func NewRpcHandler(router *router) *TRpcContext {
	handler := &TRpcContext{
		ILogger: logger,
		Router:  router,
	}
	handler.val = reflect.ValueOf(handler)
	handler.typ = handler.val.Type()
	return handler
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

func (self *TRpcContext) Handler(index ...int) handler {
	if index != nil {
		return self.route.handlers[index[0]]
	}
	return self.route.handlers[self.handlerIndex]
}

func (self *TRpcContext) Context() context.Context {
	return self.context
}

func (self *TRpcContext) IsDone() bool {
	return false
}

// the reflect model of Value
func (self *TRpcContext) ValueModel() reflect.Value {
	return self.val
}

// the reflect model of Type
func (self *TRpcContext) TypeModel() reflect.Type {
	return self.typ
}

func (self *TRpcContext) reset(rw transport.IResponse, req *transport.RpcRequest, Router IRouter, Route *route) {
	self.request = req
	self.response = rw
}

func (self *TRpcContext) setData(v interface{}) {

}

func (self *TRpcContext) String() string {
	return "RpcContext"
}
