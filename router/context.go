package router

import (
	"context"
	"reflect"
)

type (
	IContext interface {
		// pravite
		setControllerIndex(num int)
		//reset(rw IResponse, req IRequest, Router *TRouter, Route *route)
		//setData(v interface{}) // TODO 修改API名称  设置response数据

		// public
		Route() route
		Context() context.Context
		HandlerIndex() int
		Handler(index ...int) handler
		ValueModel() reflect.Value //
		TypeModel() reflect.Type
		IsDone() bool //response data is done
		String() string
	}
)
