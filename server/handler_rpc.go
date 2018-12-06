package server

import (
	"reflect"
)

type (
	// 代表一个控制集
	TRpcHandler struct {
		name string        // name of service
		rcvr reflect.Value // receiver of methods for the service
		typ  reflect.Type  // type of the receiver
		//method   map[string]*methodType   // registered methods
		//function map[string]*functionType // registered functions
	}
)

func (self *TRpcHandler) Request() IRequest {
	return nil
}

func (self *TRpcHandler) Response() IResponse {
	return nil
}

func (self *TRpcHandler) Done() bool {
	return false
}
func (self *TRpcHandler) ValueModel() reflect.Value {
	return self.rcvr
}
func (self *TRpcHandler) TypeModel() reflect.Type {
	return nil
}
