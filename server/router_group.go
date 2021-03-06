package server

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/volts-dev/logger"
	"github.com/volts-dev/utils"
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
		GetPath() string
		GetFilePath() string
		GetModulePath() string
		GetTemplateVar() map[string]interface{}
	}

	// 服务模块 每个服务代表一个对象
	TModule struct {
		*TemplateVar
		tree      *TTree
		staticDir []string
		name      string        // name of module
		rcvr      reflect.Value // receiver of methods for the module
		typ       reflect.Type  // type of the receiver
		//method   map[string]*web.TRoute   // registered methods
		//function map[string]*functionType // registered functions
		//templateVar map[string]interface{} // TODO 全局变量. 需改进

		path       string // URL 路径
		filePath   string // 短文件系统路径-当前文件夹名称
		modulePath string // 当前模块文件夹路径
		domain     string // 子域名用于区分不同域名不同路由
	}

	TemplateVar struct {
		templateVar map[string]interface{} // TODO 全局变量. 需改进

	}
)

func NewTemplateVar() *TemplateVar {
	return &TemplateVar{
		templateVar: make(map[string]interface{}),
	}
}

// set the var of the template
func (self *TemplateVar) SetTemplateVar(key string, value interface{}) {
	self.templateVar[key] = value
}

// remove the var from the template
func (self *TemplateVar) DelTemplateVar(key string) {
	delete(self.templateVar, key)
}

func (self *TemplateVar) GetTemplateVar() map[string]interface{} {
	return self.templateVar
}

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

/*    """Return the path of the given module.

Search the addons paths and return the first path where the given
module is found. If downloaded is True, return the default addons
path if nothing else is found.

"""*/
func GetModulePath(module string, downloaded bool, display_warning bool) (res string) {

	// initialize_sys_path()
	// for adp in ad_paths:
	//      if os.path.exists(opj(adp, module)) or os.path.exists(opj(adp, '%s.zip' % module)):
	//         return opj(adp, module)
	res = filepath.Join(AppPath, MODULE_DIR)
	//if _, err := os.Stat(res); err == nil {
	//	return res
	//}
	return

	// if downloaded:
	//    return opj(tools.config.addons_data_dir, module)
	//if display_warning {
	//	logger.Warnf(`module %s: module not found`, module)
	//}

	//return ""
}

/*
   """Return the full path of a resource of the given module.

   :param module: module name
   :param list(str) args: resource path components within module

   :rtype: str
   :return: absolute path to the resource

   TODO make it available inside on osv object (self.get_resource_path)
   """*/

func GetResourcePath(module_src_path string) (res string) {
	//filepath.SplitList(module_src_path)
	mod_path := GetModulePath("", false, true)

	res = filepath.Join(mod_path, module_src_path)

	if _, err := os.Stat(res); err == nil {
		return
	}

	/*
	   if  res!=="" return False
	   resource_path = opj(mod_path, *args)
	   if os.path.isdir(mod_path):
	       # the module is a directory - ignore zip behavior
	       if os.path.exists(resource_path):
	           return resource_path
	*/
	return ""
}

// new a module
// default url path :/{mod_name}
// default file path :/{mod_name}/{static}/
// default tmpl path :/{mod_name{/{tmpl}/
func NewModule(paths ...interface{}) *TModule {
	mod := &TModule{
		//templateVar: make(map[string]interface{}),
		TemplateVar: NewTemplateVar(),
	}

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

func (self *TModule) GetModulePath() string {
	return self.modulePath
}

func (self *TModule) SetPath(path string) {
	self.path = path
}

func (self *TModule) SetFilePath(path string) {
	self.filePath = path
}

func (self *TModule) SetModulePath(path string) {
	self.modulePath = path
}

// Static serves files from the given file system root.
// Internally a http.FileServer is used, therefore http.NotFound is used instead
// of the Router's NotFound handler.
// To use the operating system's file system implementation,
// use :
//     router.Static("/static", "/var/www")
func (self *TModule) SetStatic(path, root string) {

}

// set the var of the template
func (self *TModule) SetTemplateVar(key string, value interface{}) {
	self.templateVar[key] = value
}

// remove the var from the template
func (self *TModule) DelTemplateVar(key string) {
	delete(self.templateVar, key)
}

func (self *TModule) GetTemplateVar() map[string]interface{} {
	return self.templateVar
}

// !NOTE! RPC 或者 HTTP 不适用同一Module注册路由
/*  Add route with method
HTTP: "GET/POST/DELETE/PUT/HEAD/OPTIONS/REST"
RPC: "CONNECT"

Match rules
Base: (type:name) if difine type than the route only match the string same to the type
Example: string:id only match "abc"
         int:id only match number "123"
         :id could match all kind of type with name id
'/web/content/(string:xmlid)',
'/web/content/(string:xmlid)/(string:filename)',
'/web/content/(int:id)',
'/web/content/(int:id)/(string:filename)',
'/web/content/(int:id)-<string:unique)',
'/web/content/(int:id)-<string:unique)/(string:filename)',
'/web/content/(string:model)/(int:id)/(string:field)',
'/web/content/(string:model)/(int:id)/(string:field)/(string:filename)'
for details please read tree.go */
func (self *TModule) Url(method string, path string, controller interface{}) *TRoute {
	method = strings.ToUpper(method)
	switch method {
	case "GET":
		return self.url(CommomRoute, []string{"GET", "HEAD"}, &TUrl{Path: path}, controller)

	case "POST", "PUT", "HEAD", "OPTIONS", "TRACE", "PATCH", "DELETE", "REST":
		return self.url(CommomRoute, []string{method}, &TUrl{Path: path}, controller)

	case "CONNECT": // RPC WS
		return self.url(CommomRoute, []string{method}, &TUrl{Path: path}, controller)

	default:
		panic(fmt.Sprintf("the params in Module.Url() %v:%v is invaild", method, path))
	}
}

/*
pos: true 为插入Before 反之After
*/
func (self *TModule) url(routeType RouteType, methods []string, url *TUrl, controller interface{}) *TRoute {
	// check vaild
	if routeType != ProxyRoute && controller == nil {
		panic("the route must binding a controller!")
	}

	// init Value and Type
	ctrl_value, ok := controller.(reflect.Value)
	if !ok {
		ctrl_value = reflect.ValueOf(controller)
	}

	// transfer prt to struct
	if ctrl_value.Kind() == reflect.Ptr {
		ctrl_value = ctrl_value.Elem()
	}
	ctrl_type := ctrl_value.Type()
	if ctrl_type.Kind() == reflect.Struct || ctrl_type.Kind() == reflect.Ptr {
		object_name := utils.DotCasedName(utils.Obj2Name(controller))

		n := ctrl_type.NumMethod()
		var (
			name   string
			method reflect.Value
			typ    reflect.Type
		)

		isREST := utils.InStrings("REST", methods...) > -1
		for i := 0; i < n; i++ {
			// get the method information from the ctrl Type
			name = ctrl_type.Method(i).Name
			//ctrl_type = ctrl_type.Elem()
			method = ctrl_type.Method(i).Func
			typ = method.Type()
			logger.Dbg("me", method.String())
			if method.CanInterface() {
				//log.Dbg("RegisterName", object_name, name, method)
				// 添加注册方法
				if isREST {
					// 方法为对象方法名称 url 只注册对象名称
					self.url(routeType, []string{name}, &TUrl{Path: url.Path, Controller: object_name, Action: name}, method)
				} else {
					// the rpc method needs one output.
					if typ.NumOut() != 1 {
						//log.Info("method", name, "has wrong number of outs:", typ.NumOut())
						continue
					}

					self.url(routeType, methods, &TUrl{Path: strings.Join([]string{url.Path, name}, "."), Controller: object_name, Action: name}, method)
				}
			}
		}

		// the end of the struct mapping
		return nil //TODO 返回路由

	} else if ctrl_type.Kind() != reflect.Func {
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
		Action:   url.Action, //
		Type:     routeType,
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
	if utils.InStrings("CONNECT", methods...) > -1 {
		// !NOTE! 修改分隔符
		if self.tree.DelimitChar == 0 {
			self.tree.DelimitChar = '.'
		}

		// Method must be exported.
		if ctrl_type.PkgPath() != "" {
			return route
		}

		logger.Dbg("NumIn", ctrl_type.NumIn(), ctrl_type.String())
		// Method needs four ins: receiver, context.Context, *args, *reply.
		if ctrl_type.NumIn() != 4 {
			panic(fmt.Sprintf(`method "%v" has wrong number of ins: %v %v`, url.Action, ctrl_type.In(0), ctrl_type.NumIn()))
		}
		/*if ctrl_type.NumIn() != 3 {
			panic(fmt.Sprintf(`registerFunction: has wrong number of ins: %s`, ctrl_type.NumIn()))
			return route
		}*/
		if ctrl_type.NumOut() != 1 {
			panic(fmt.Sprintf(`registerFunction: has wrong number of outs: %v`, ctrl_type.NumOut()))
		}

		// First arg must be context.Context
		ctxType := ctrl_type.In(1)
		if ctxType != reflect.TypeOf(new(TRpcHandler)) {
			//log.Info("method", url.Action, " must use context.Context as the first parameter")
			return nil
		}

		// Second arg need not be a pointer.
		argType := ctrl_type.In(2)
		if !isExportedOrBuiltinType(argType) {
			//log.Info(url.Action, "parameter type not exported:", argType)
			return nil
		}
		// Third arg must be a pointer.
		replyType := ctrl_type.In(3)
		if replyType.Kind() != reflect.Ptr {
			//log.Info("method", url.Action, "reply type not a pointer:", replyType)
			return nil
		}

		mt.ArgType = argType
		mt.ReplyType = replyType
	}

	route.MainCtrl = mt
	route.Ctrls = append(route.Ctrls, route.MainCtrl)

	// register route
	for _, m := range methods {
		self.tree.AddRoute(strings.ToUpper(m), url.Path, route)
	}

	return route
}
