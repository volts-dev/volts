package server

import (
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/VectorsOrigin/logger"

	//	"context"
	//	"fmt"
	"reflect"
	"unicode"
	"unicode/utf8"

	//	"sync"

	log "github.com/VectorsOrigin/logger"
	"github.com/VectorsOrigin/utils"
)

type (
	// [scheme:][//[userinfo@]host][/[path]/controller.action][?query][#fragment]
	TUrl struct {
		Scheme string
		Opaque string // encoded opaque data
		//User       *Userinfo // username and password information
		Host       string // host or host:port
		Path       string // path (relative paths may omit leading slash)
		Controller string
		Action     string
		//RawPath    string    // encoded path hint (see EscapedPath method); added in Go 1.5
		//ForceQuery bool      // append a query ('?') even if RawQuery is empty; added in Go 1.7
		//RawQuery   string    // encoded query values, without '?'
		//Fragment   string    // fragment for references, without '#'
	}

	IModule interface {
		// 返回Module所有Routes 理论上只需被调用一次
		GetRoutes() *TTree
	}

	// 服务模块 每个服务代表一个对象
	TModule struct {
		tree *TTree
		name string        // name of module
		rcvr reflect.Value // receiver of methods for the module
		typ  reflect.Type  // type of the receiver
		//method   map[string]*web.TRoute   // registered methods
		//function map[string]*functionType // registered functions

		path     string // URL 路径
		filePath string // 短文件系统路径-当前文件夹名称
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

// get current file path without file name
func cur_path() string {
	_, file, _, _ := runtime.Caller(2) // level 3
	path, _ := path.Split(file)
	return path
}

// 随文件引用层次而变
// get current file dir name
func cur_dir_name() string {
	_, file, _, _ := runtime.Caller(3) // level 3
	path, _ := path.Split(file)
	return filepath.Base(path)
}

func NewModule(paths ...interface{}) *TModule {
	mod := &TModule{}

	// 获取路径文件夹名称
	_, file, _, _ := runtime.Caller(1) // level 3
	file_path, _ := path.Split(file)
	dir_name := filepath.Base(file_path) // TODO 过滤验证文件夹名称
	//log.Dbg("vv", dir_name, file_path)
	//TODO　验证文件夹名称

	// 模块名称唯一的
	mod.name = dir_name
	mod.path = dir_name     // AaAaa>aa_aaa
	mod.filePath = dir_name // static files path
	if len(paths) != 0 {
		mod.path = utils.DotCasedName(utils.Obj2Name(paths[0])) // AaAaa>aa_aaa

		// 修改为指定文件路径
		if len(paths) == 2 {
			mod.filePath = paths[1].(string)
		}
	}

	// init router tree
	mod.tree = NewRouteTree()
	mod.tree.IgnoreCase = true
	//mod.tree.DelimitChar = '.' // 修改为xxx.xxx

	return mod
}

func (self *TModule) Name() string {
	return self.name
}

func (self *TModule) GetRoutes() *TTree {
	return self.tree
}

func (self *TModule) GetPath() string {
	return self.path
}

func (self *TModule) GetFilePath() string {
	return self.filePath
}

// 重组添加模块[URL]
func (self *TModule) Url(method string, path string, controller interface{}) *TRoute {
	switch strings.ToUpper(method) {
	case "GET":
		return self.url(CommomRoute, []string{"GET", "HEAD"}, &TUrl{Path: path}, controller)

	case "POST", "PUT", "HEAD", "OPTIONS", "TRACE", "PATCH", "DELETE", "REST":
		return self.url(CommomRoute, []string{method}, &TUrl{Path: path}, controller)

	case "CONNECT": // RPC WS
		return self.url(CommomRoute, []string{method}, &TUrl{Path: path}, controller)

	default:
		log.Panic("the params in Module.Url() %V:%V is invaild", method, path)
	}

	return nil
}

/*
pos: true 为插入Before 反之After
*/
func (self *TModule) url(route_type RouteType, methods []string, url *TUrl, controller interface{}) *TRoute {
	// check vaild
	if route_type != ProxyRoute && controller == nil {
		logger.Panic("the route must binding a controller!")
	}

	ctrl_value, ok := controller.(reflect.Value)
	if !ok {
		ctrl_value = reflect.ValueOf(controller)
	}
	ctrl_type := ctrl_value.Type()

	if ctrl_value.Kind() == reflect.Struct {
		object_name := utils.DotCasedName(utils.Obj2Name(controller))

		n := ctrl_type.NumMethod()
		var (
			name   string
			method reflect.Value
			typ    reflect.Type
		)

		isREST := utils.InStrings("REST", methods...) > 0

		for i := 0; i < n; i++ {
			name = ctrl_type.Method(i).Name
			method = ctrl_value.Method(i)
			typ = method.Type()

			// the rpc method needs one output.
			if isREST && typ.NumOut() != 1 {
				//log.Info("method", name, "has wrong number of outs:", typ.NumOut())
				continue
			}

			if method.CanInterface() {
				//log.Dbg("RegisterName", object_name, name, method)
				// 添加注册方法
				if isREST {
					// 方法为对象方法名称 url 只注册对象名称
					self.url(route_type, []string{typ.Name()}, &TUrl{Path: url.Path, Controller: object_name}, method)
				} else {
					self.url(route_type, methods, &TUrl{Path: url.Path, Controller: object_name, Action: name}, method)
				}
			}
		}

		// the end of the struct mapping
		return nil //TODO 返回路由

	} else if ctrl_value.Kind() != reflect.Func {
		panic("controller must be func or bound method")

	}

	// trim the Url to including "/" on begin of path
	if !strings.HasPrefix(url.Path, "/") && url.Path != "/" {
		url.Path = "/" + url.Path
	}

	route := &TRoute{
		Url:      url,
		Path:     url.Path,
		FilePath: self.filePath,
		Model:    self.name,
		Action:   "", //
		Type:     route_type,
		Ctrls:    make([]TMethodType, 0),
		//HookCtrl: make([]TMethodType, 0),
		//Host:     host,
		//Scheme:   scheme,

	}

	/*// # is it proxy route
	if scheme != "" && host != "" {
		route.Host = &urls.path{
			Scheme: scheme,
			Host:   host,
		}
		route.isReverseProxy = true
	}
	*/
	mt := TMethodType{
		Func:     ctrl_value,
		FuncType: ctrl_type}

	// RPC route validate
	if utils.InStrings("CONNECT", methods...) > 1 {
		// Method must be exported.
		if ctrl_type.PkgPath() != "" {
			return route
		}

		log.Dbg("NumIn", ctrl_type.NumIn(), ctrl_type.String())
		// Method needs four ins: receiver, context.Context, *args, *reply.
		if ctrl_type.NumIn() != 4 {
			log.Info("method", url.Action, "has wrong number of ins:", ctrl_type.NumIn())
		}
		if ctrl_type.NumIn() != 3 {
			log.Infof("rpcx.registerFunction: has wrong number of ins: %s", ctrl_type.String())
			return route
		}
		if ctrl_type.NumOut() != 1 {
			log.Infof("rpcx.registerFunction: has wrong number of outs: %s", ctrl_type.String())
			return route
		}

		// First arg must be context.Context
		ctxType := ctrl_type.In(0)
		if !ctxType.Implements(typeOfContext) {
			log.Info("method", url.Action, " must use context.Context as the first parameter")
			return route
		}

		// Second arg need not be a pointer.
		argType := ctrl_type.In(1)
		if !isExportedOrBuiltinType(argType) {
			log.Info(url.Action, "parameter type not exported:", argType)
			return route
		}
		// Third arg must be a pointer.
		replyType := ctrl_type.In(2)
		if replyType.Kind() != reflect.Ptr {
			log.Info("method", url.Action, "reply type not a pointer:", replyType)
			return route
		}

		mt.ArgType = argType
		mt.ReplyType = replyType
	}

	route.MainCtrl = mt
	route.Ctrls = append(route.Ctrls, route.MainCtrl)

	// register route
	for _, m := range methods {
		self.tree.AddRoute(m, url.Path, route)
	}

	return route
}
