package server

import (
	"reflect"
)

type (
	IHandler interface {
		Request() IRequest
		Response() IResponse
		ValueModel() reflect.Value
		TypeModel() reflect.Type
		Done() bool //response data is done
	}
)
