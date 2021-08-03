package server

import (
	"context"
	"reflect"

	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/transport"
)

var (
	RpcHandlerType = reflect.TypeOf(RpcHandler{})
)

type (
	// 代表一个控制集
	RpcHandler struct {
		logger.ILogger
		context         context.Context
		response        transport.IResponse   //http.ResponseWriter
		request         *transport.RpcRequest //
		Router          *router
		Route           TRoute //执行本次Handle的Route
		inited          bool
		controllerIndex int
		name            string        // name of service
		__rcvr          reflect.Value // receiver of methods for the service
		val             reflect.Value
		typ             reflect.Type // type of the receiver

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

func NewRpcHandler(router *router) *RpcHandler {
	handler := &RpcHandler{
		ILogger: router.server.config.Logger,
		Router:  router,
	}
	handler.val = reflect.ValueOf(handler)
	handler.typ = handler.val.Type()
	return handler
}

func (self *RpcHandler) setControllerIndex(num int) {
	self.controllerIndex = num
}

func (self *RpcHandler) ControllerIndex() int {
	return self.controllerIndex
}

func (self *RpcHandler) Context() context.Context {
	return self.context
}

func (self *RpcHandler) IsDone() bool {
	return false
}

// the reflect model of Value
func (self *RpcHandler) ValueModel() reflect.Value {
	return self.val
}

// the reflect model of Type
func (self *RpcHandler) TypeModel() reflect.Type {
	return self.typ
}

func (self *RpcHandler) reset(rw transport.IResponse, req *transport.RpcRequest, Router IRouter, Route *TRoute) {
	self.request = req
	self.response = rw
}

func (self *RpcHandler) setData(v interface{}) {

}
