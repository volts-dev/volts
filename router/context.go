package router

import (
	"context"
	"reflect"

	"github.com/volts-dev/volts/util/body"
)

type (
	IContext interface {
		// pravite
		setControllerIndex(num int)
		//reset(rw IResponse, req IRequest, Router *TRouter, Route *route)
		//setData(v interface{}) // TODO 修改API名称  设置response数据

		// public
		Body() *body.TBody
		Write([]byte) (int, error)
		WriteStream(interface{}) error
		Route() route
		Router() IRouter
		Context() context.Context

		RespondByJson(data interface{})
		//Abort(string)
		HandlerIndex() int
		Handler(index ...int) handler
		ValueModel() reflect.Value //
		TypeModel() reflect.Type
		IsDone() bool //response data is done
		String() string
	}
)
