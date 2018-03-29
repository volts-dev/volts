package server

import (
	//	"context"
	//	"fmt"
	"reflect"
	//	"sync"
	log "vectors/logger"
	"vectors/utils"
	"vectors/web"
)

type (
	// 服务模块 每个服务代表一个对象
	TModule struct {
		tree *web.TTree
		name string        // name of service
		rcvr reflect.Value // receiver of methods for the service
		typ  reflect.Type  // type of the receiver
		//method   map[string]*web.TRoute   // registered methods
		//function map[string]*functionType // registered functions
	}
)

func NewModule() *TModule {
	return &TModule{
		tree: web.NewRouteTree(),
	}
}

// suitableMethods returns suitable Rpc methods of typ, it will report
// error using log if reportErr is true.
func (self *TModule) __suitableMethods(typ reflect.Type, reportErr bool) {
	/*	methods := make(map[string]*methodType)
		for m := 0; m < typ.NumMethod(); m++ {
			method := typ.Method(m)
			mtype := method.Type
			mname := method.Name
			// Method must be exported.
			if method.PkgPath != "" {
				continue
			}
			// Method needs four ins: receiver, context.Context, *args, *reply.
			if mtype.NumIn() != 4 {
				if reportErr {
					log.Info("method", mname, "has wrong number of ins:", mtype.NumIn())
				}
				continue
			}
			// First arg must be context.Context
			ctxType := mtype.In(1)
			if !ctxType.Implements(typeOfContext) {
				if reportErr {
					log.Info("method", mname, " must use context.Context as the first parameter")
				}
				continue
			}

			// Second arg need not be a pointer.
			argType := mtype.In(2)
			if !isExportedOrBuiltinType(argType) {
				if reportErr {
					log.Info(mname, "parameter type not exported:", argType)
				}
				continue
			}
			// Third arg must be a pointer.
			replyType := mtype.In(3)
			if replyType.Kind() != reflect.Ptr {
				if reportErr {
					log.Info("method", mname, "reply type not a pointer:", replyType)
				}
				continue
			}
			// Reply type must be exported.
			if !isExportedOrBuiltinType(replyType) {
				if reportErr {
					log.Info("method", mname, "reply type not exported:", replyType)
				}
				continue
			}
			// Method needs one out.
			if mtype.NumOut() != 1 {
				if reportErr {
					log.Info("method", mname, "has wrong number of outs:", mtype.NumOut())
				}
				continue
			}
			// The return type of the method must be error.
			if returnType := mtype.Out(0); returnType != typeOfError {
				if reportErr {
					log.Info("method", mname, "returns", returnType.String(), "not error")
				}
				continue
			}
			methods[mname] = &methodType{method: method, ArgType: argType, ReplyType: replyType}

			//	argsReplyPools.Init(argType)
			//	argsReplyPools.Init(replyType)
		}

		//	self.method = methods

		// 错误提示
		/*
			if len(self.method) == 0 {
				var errorStr string

				// To help the user, see if a pointer receiver would work.
				method := suitableMethods(reflect.PtrTo(service.typ), false)
				if len(method) != 0 {
					errorStr = "rpcx.Register: type " + sname + " has no exported methods of suitable type (hint: pass a pointer to value of that type)"
				} else {
					errorStr = "rpcx.Register: type " + sname + " has no exported methods of suitable type"
				}
				log.Error(errorStr)
				return errors.New(errorStr)
			}*/

	return

}

// 添加路由到Tree
// AddFunction publish a func or bound method
// name is the method name
// function is a func or bound method
// option includes Mode, Simple, Oneway and NameSpace
func (self *TModule) addFunc(aType web.RouteType, path, name string, function interface{}) {
	if name == "" {
		panic("name can't be empty")
	}
	if function == nil {
		panic("function can't be nil")
	}
	f, ok := function.(reflect.Value)
	if !ok {
		f = reflect.ValueOf(function)
	}
	if f.Kind() != reflect.Func {
		panic("function must be func or bound method")
	}
	/*
		var options Options
		if len(option) > 0 {
			options = option[0]
		}
		if options.NameSpace != "" && name != "*" {
			name = options.NameSpace + "_" + name
		}*/

	//mm.mmLocker.Lock()
	//if self.method[strings.ToLower(name)] == nil {
	//	mm.MethodNames = append(mm.MethodNames, name)
	//}

	route := &web.TRoute{
		Path: path,
		//FilePath: "",
		Model:  self.name,
		Action: name, //
		Type:   aType,
		//HookCtrl: make(map[string][]web.TMethodType),
		//Ctrls:    make(map[string][]web.TMethodType),
		//Host:     host,
		//Scheme:   scheme,
	}
	t := f.Type()
	mt := web.TMethodType{
		Func:     f,
		FuncType: t}

	for i := 0; i < t.NumIn(); i++ {
		mt.ArgType = append(mt.ArgType, t.In(i)) // 添加参数类型
	}

	// return 值
	for i := 0; i < t.NumOut(); i++ {
		mt.ReplyType = append(mt.ReplyType, t.Out(i)) // 添加参数类型
	}

	route.MainCtrl = mt

	self.tree.AddRoute("rpc", path+"."+name, route)
	//self.method[strings.ToLower(name)] = route
	//mm.mmLocker.Unlock()
}

func (self *TModule) Register(obj interface{}) {
	if obj == nil {
		panic("obj can't be nil")
	}
	v := reflect.ValueOf(obj)
	t := v.Type()

	object_name := utils.DotCasedName(utils.Obj2Name(obj))
	n := t.NumMethod()
	var (
		name   string
		method reflect.Value
		typ    reflect.Type
	)
	for i := 0; i < n; i++ {
		name = t.Method(i).Name
		method = v.Method(i)
		typ = method.Type()
		// Method needs one out.
		if typ.NumOut() != 1 {
			log.Info("method", name, "has wrong number of outs:", typ.NumOut())
			continue
		}

		if method.CanInterface() {
			// 添加注册方法
			self.addFunc(web.CommomRoute, object_name, name, method)
		}
	}
}
