package router

import (
	"context"
	"reflect"

	"github.com/volts-dev/dataset"
	"github.com/volts-dev/volts/util/body"
)

var ContextType = reflect.TypeOf(new(IContext)).Elem()

type (
	TParamsSet struct {
		dataset.TRecordSet
		context IContext
	}

	IContext interface {
		// pravite
		//setControllerIndex(num int)
		setHandler(*handler)
		//reset(rw IResponse, req IRequest, Router *TRouter, Route *route)
		//setData(v interface{}) // TODO 修改API名称  设置response数据

		// public
		Next()
		Body() *body.TBody
		Data() *TParamsSet
		Write([]byte) (int, error)
		WriteStream(interface{}) error
		Route() route
		Router() IRouter
		Context() context.Context
		RespondByJson(data interface{})
		HandlerIndex() int
		Handler(index ...int) *handler
		ValueModel() reflect.Value //
		TypeModel() reflect.Type
		IsDone() bool //response data is done
		String() string
		//Abort(string)
	}
)

func NewParamsSet(hd IContext) *TParamsSet {
	return &TParamsSet{
		TRecordSet: *dataset.NewRecordSet(),
		context:    hd,
	}
}
