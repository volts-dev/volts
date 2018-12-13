package server

import (
	"reflect"
	"vectors/volts"
)

var (
	RpcHandlerType = reflect.TypeOf(TRpcHandler{})
)

type (
	// 代表一个控制集
	TRpcHandler struct {
		volts.ILogger
		name string        // name of service
		rcvr reflect.Value // receiver of methods for the service
		typ  reflect.Type  // type of the receiver
		//method   map[string]*methodType   // registered methods
		//function map[string]*functionType // registered functions

		replyv reflect.Value
	}
)

func NewRpcHandler(router *TRouter) *TRpcHandler {
	return &TRpcHandler{}
}
func (self *TRpcHandler) Request() IRequest {
	return nil
}

func (self *TRpcHandler) Response() IResponse {
	return nil
}

func (self *TRpcHandler) Done() bool {
	return false
}

// the reflect model of Value
func (self *TRpcHandler) ValueModel() reflect.Value {
	return self.rcvr
}

// the reflect model of Type
func (self *TRpcHandler) TypeModel() reflect.Type {
	return self.typ
}
func (self *TRpcHandler) connect(rw IResponse, req IRequest, Router *TRouter, Route *TRoute) {

}
func (self *TRpcHandler) setData(v interface{}) {

}
