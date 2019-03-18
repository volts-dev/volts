package server

import (
	//	"context"
	//	"fmt"
	"reflect"
	"unicode"
	"unicode/utf8"

	//	"sync"
	"github.com/volts-dev/web"

	log "github.com/volts-dev/logger"
	"github.com/volts-dev/utils"
)

type (
	IModule interface {
		// 返回Module所有Routes 理论上只需被调用一次
		GetRoutes() *web.TTree
	}

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

func isExported(name string) bool {
	rune, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(rune)
}

func isExportedOrBuiltinType(t reflect.Type) bool {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	// PkgPath will be non-empty even for an exported type,
	// so we need to check the type name as well.
	return isExported(t.Name()) || t.PkgPath() == ""
}

func NewModule() *TModule {
	tree := web.NewRouteTree()
	tree.IgnoreCase = true
	tree.DelimitChar = '.' // 修改为xxx.xxx

	return &TModule{
		tree: tree,
	}
}

func (self *TModule) GetRoutes() *web.TTree {
	return self.tree
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
					errorStr = "rpc.Register: type " + sname + " has no exported methods of suitable type (hint: pass a pointer to value of that type)"
				} else {
					errorStr = "rpc.Register: type " + sname + " has no exported methods of suitable type"
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
	method := f.Type()
	mt := web.TMethodType{
		Func:     f,
		FuncType: method}

	/*
		for i := 0; i < t.NumIn(); i++ {
			mt.ArgType = append(mt.ArgType, t.In(i)) // 添加参数类型
		}
			// return 值
		for i := 0; i < t.NumOut(); i++ {
			mt.ReplyType = append(mt.ReplyType, t.Out(i)) // 添加参数类型
		}

	*/
	// Method must be exported.
	if method.PkgPath() != "" {
		return
	}

	log.Dbg("NumIn", method.NumIn(), method.String())
	// Method needs four ins: receiver, context.Context, *args, *reply.
	if method.NumIn() != 4 {
		log.Info("method", name, "has wrong number of ins:", method.NumIn())
	}
	if method.NumIn() != 3 {
		log.Infof("rpc.registerFunction: has wrong number of ins: %s", method.String())
		return
	}
	if method.NumOut() != 1 {
		log.Infof("rpc.registerFunction: has wrong number of outs: %s", method.String())
		return
	}

	// First arg must be context.Context
	ctxType := method.In(0)
	if !ctxType.Implements(typeOfContext) {
		log.Info("method", name, " must use context.Context as the first parameter")
		return
	}

	// Second arg need not be a pointer.
	argType := method.In(1)
	if !isExportedOrBuiltinType(argType) {
		log.Info(name, "parameter type not exported:", argType)
		return
	}
	// Third arg must be a pointer.
	replyType := method.In(2)
	if replyType.Kind() != reflect.Ptr {

		log.Info("method", name, "reply type not a pointer:", replyType)

		return
	}

	mt.ArgType = argType
	mt.ReplyType = replyType

	route.MainCtrl = mt
	route.Ctrls = append(route.Ctrls, route.MainCtrl)

	log.Dbg("addFunc", path+"."+name)
	self.tree.AddRoute("HEAD", path+"."+name, route)
	//self.method[strings.ToLower(name)] = route
	//mm.mmLocker.Unlock()
}

func (self *TModule) RegisterFunction(object_name, name string, function interface{}) {
	self.addFunc(web.CommomRoute, object_name, name, function)
}

func (self *TModule) RegisterName(object_name string, obj interface{}) {
	if obj == nil {
		panic("obj can't be nil")
	}
	v := reflect.ValueOf(obj)
	t := v.Type()
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
			log.Dbg("RegisterName", object_name, name, method)
			// 添加注册方法
			self.addFunc(web.CommomRoute, object_name, name, method)
		}
	}
}

// 注册对象
func (self *TModule) Register(obj interface{}) {
	object_name := utils.DotCasedName(utils.Obj2Name(obj))
	self.RegisterName(object_name, obj)
}
