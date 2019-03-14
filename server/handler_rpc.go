package server

import (
	"context"
	"reflect"

	"github.com/volts-dev/volts"
	"github.com/volts-dev/volts/server/listener/rpc"
)

var (
	RpcHandlerType = reflect.TypeOf(TRpcHandler{})
)

type (
	// 代表一个控制集
	TRpcHandler struct {
		volts.ILogger
		context  context.Context
		response rpc.Response //http.ResponseWriter
		request  *rpc.Request //
		Router   *TRouter
		Route    *TRoute //执行本次Handle的Route

		name   string        // name of service
		__rcvr reflect.Value // receiver of methods for the service
		val    reflect.Value
		typ    reflect.Type // type of the receiver

		//method   map[string]*methodType   // registered methods
		//function map[string]*functionType // registered functions
		argv   reflect.Value
		replyv reflect.Value
	}
)

func NewRpcHandler(router *TRouter) *TRpcHandler {
	handler := &TRpcHandler{
		ILogger: router.server.logger,
		Router:  router,
	}
	handler.val = reflect.ValueOf(handler)
	handler.typ = handler.val.Type()
	return handler
}

func (self *TRpcHandler) Request() IRequest {
	return nil
}

func (self *TRpcHandler) Response() IResponse {
	return nil
}

func (self *TRpcHandler) IsDone() bool {
	return false
}

// the reflect model of Value
func (self *TRpcHandler) ValueModel() reflect.Value {
	return self.val
}

// the reflect model of Type
func (self *TRpcHandler) TypeModel() reflect.Type {
	return self.typ
}

func (self *TRpcHandler) reset(rw IResponse, req IRequest, Router *TRouter, Route *TRoute) {
	self.request = req.(*rpc.Request)
	self.response = rw.(rpc.Response)
	self.Route = Route
}

func (self *TRpcHandler) setData(v interface{}) {

}
