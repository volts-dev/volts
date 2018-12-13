package server

import (
	"reflect"
)

type (
	IHandler interface {
		// pravite
		connect(rw IResponse, req IRequest, Router *TRouter, Route *TRoute)
		setData(v interface{}) // TODO 修改API名称  设置response数据

		// public
		Request() IRequest
		Response() IResponse
		ValueModel() reflect.Value //
		TypeModel() reflect.Type
		Done() bool //response data is done
	}
)
