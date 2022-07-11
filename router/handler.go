package router

import (
	"reflect"

	"github.com/volts-dev/volts/registry"
)

type (
	// 路由节点绑定的控制器 func(Handler)
	handler struct {
		Name       string        // 名称
		Func       reflect.Value // 方法本体
		FuncType   reflect.Type  // 方法类型
		ArgType    reflect.Type  // 参数组类型
		ReplyType  reflect.Type  // TODO 返回多结果
		Controller reflect.Value // TODO 待定 控制器
		//ArgType   []reflect.Type // 参数组类型
		//ReplyType []reflect.Type //TODO 返回多结果
		Type     HandlerType         // Route 类型 决定合并的形式
		Services []*registry.Service // 该路由服务线路 提供网关等特殊服务用// Versions of this service
		// test
		Funcs []reflect.Value // 方法本体
	}
)

/*
@controller:本地服务会自行本地控制程序 其他代理远程服务为nil
*/
func newHandler(hanadlerType HandlerType, controller interface{}, services []*registry.Service) handler {
	h := handler{
		Type:     hanadlerType,
		Services: services,
	}

	// init Value and Type
	if controller != nil {
		ctrl_value, ok := controller.(reflect.Value)
		if !ok {
			ctrl_value = reflect.ValueOf(controller)
		}
		h.Func = ctrl_value
		h.FuncType = ctrl_value.Type()
	}

	return h
}
