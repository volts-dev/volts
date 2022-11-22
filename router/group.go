package router

import (
	"fmt"
	"net/http"
	"os"
	"path"
	_path "path"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/volts-dev/utils"
	"github.com/volts-dev/volts/config"
)

type (

	// [scheme:][//[userinfo@]host][/[path]/controller.action][?query][#fragment]
	TUrl struct {
		Scheme     string
		Opaque     string // encoded opaque data
		Host       string // host or host:port
		Path       string // path (relative paths may omit leading slash)
		Controller string
		Action     string
		//RawPath    string    // encoded path hint (see EscapedPath method); added in Go 1.5
		//ForceQuery bool      // append a query ('?') even if RawQuery is empty; added in Go 1.7
		//RawQuery   string    // encoded query values, without '?'
		//Fragment   string    // fragment for references, without '#'
	}

	// 非volts通用接口
	IGroup interface {
		// 返回Module所有Routes 理论上只需被调用一次
		GetRoutes() *TTree
		GetPath() string
		GetFilePath() string
		GetTemplateVar() map[string]interface{}
	}

	// 服务模块 每个服务代表一个对象
	TGroup struct {
		*TemplateVar
		config     *GroupConfig
		middleware *TMiddlewareManager
		tree       *TTree
		rcvr       reflect.Value // receiver of methods for the module
		typ        reflect.Type  // type of the receiver
		path       string        // URL 路径
		//modulePath string // 当前模块文件夹路径
		domain string // 子域名用于区分不同域名不同路由
	}

	TemplateVar struct {
		templateVar map[string]interface{} // TODO 全局变量. 需改进
	}
)

/*
	"""Return the path of the given module.

Search the addons paths and return the first path where the given
module is found. If downloaded is True, return the default addons
path if nothing else is found.

"""
*/
func GetModulePath(module string, downloaded bool, display_warning bool) (res string) {

	// initialize_sys_path()
	// for adp in ad_paths:
	//      if os.path.exists(opj(adp, module)) or os.path.exists(opj(adp, '%s.zip' % module)):
	//         return opj(adp, module)
	res = filepath.Join(config.AppPath, MODULE_DIR)
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

func newTemplateVar() *TemplateVar {
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

// get current source codes file path without file name
func curFilePath(skip int) (string, string) {
	/*s := 0
	for {
		if pc, file, _, ok := runtime.Caller(s); ok {
			// Note that the test line may be different on
			// distinct calls for the same test.  Showing
			// the "internal" line is helpful when debugging.
			logger.Dbg(config.AppPath, pc, " ", file, s)
		} else {
			break
		}
		s += 1
	}*/
	_, file, _, _ := runtime.Caller(skip)
	filePath, _ := _path.Split(file)
	dirName := filepath.Base(filePath) // TODO 过滤验证文件夹名称
	//log.Dbg(config.AppPath, " ", filePath)
	// 过滤由router创建的组群
	if dirName == "router" {
		return config.AppPath, "" // filepath.Base(AppPath)
	}

	// 废除 有BUG // 确保路径在APP内
	//if !strings.HasPrefix(filePath, config.AppPath) {
	//	log.Err("Get current group path failed!")
	//	return "", ""
	//}

	return filePath, dirName
}

// new a module
// default url path :/{mod_name}
// default file path :/{mod_name}/{static}/
// default tmpl path :/{mod_name{/{tmpl}/
func NewGroup(opts ...GroupOption) *TGroup {
	cfg := &GroupConfig{}

	for _, opt := range opts {
		opt(cfg)
	}

	// 获取路径文件夹名称
	if cfg.Name == "" || cfg.FilePath == "" {
		cfg.FilePath, cfg.Name = curFilePath(2)
	}

	grp := &TGroup{
		config:      cfg,
		TemplateVar: newTemplateVar(),
		tree:        NewRouteTree(WithIgnoreCase()),
		path:        _path.Join("/", cfg.Name),
	}

	// init router tree
	grp.SetStatic("/static")
	return grp
}

func (self *TGroup) Name() string {
	return self.config.Name
}

func (self *TGroup) GetRoutes() *TTree {
	return self.tree
}

// the URL path
func (self *TGroup) GetPath() string {
	return self.path
}

// 获取组的绝对路径
func (self *TGroup) GetFilePath() string {
	return self.config.FilePath
}

// 获取组的链接路径
func (self *TGroup) SetPath(path string) {
	self.path = path
}

func (self *TGroup) SetFilePath(path string) {
	self.config.FilePath = path
}

// Static serves files from the given file system root.
// Internally a http.FileServer is used, therefore http.NotFound is used instead
// of the Router's NotFound handler.
// To use the operating system's file system implementation,
// use :
//
//	router.Static("/static", "/var/www")
func (self *TGroup) SetStatic(relativePath string, root ...string) {
	if strings.Contains(relativePath, ":") || strings.Contains(relativePath, "*") {
		panic("URL parameters can not be used when serving a static folder")
	}

	fp := self.config.FilePath
	if len(root) > 0 {
		fp = root[0]
	}

	fs := http.Dir(_path.Join(fp, relativePath)) // the filesystem path
	// 模版变量
	urlPattern := _path.Join(self.path, relativePath)
	self.SetTemplateVar(relativePath[1:], urlPattern)                 // set the template var value
	absolutePath := _path.Join(self.config.PrefixPath, urlPattern)    // the url path
	fileServer := http.StripPrefix(absolutePath, http.FileServer(fs)) // binding a file server

	handler := func(c *THttpContext) {
		file := filepath.Join(c.pathParams.FieldByName("filepath").AsString())
		// Check if file exists and/or if we have permission to access it
		if _, err := fs.Open(file); err != nil {
			// 对最后一个控制器返回404
			if c.handlerIndex == len(c.route.handlers)-1 {
				c.response.WriteHeader(http.StatusNotFound)
			}

			log.Err(err)
			return
		}

		fileServer.ServeHTTP(c.response, c.request.Request)
		c.Apply() //已经服务当前文件并结束
	}

	// 路由路径
	urlPattern = _path.Join(self.config.PrefixPath, urlPattern, fmt.Sprintf("/%s:filepath%s", string(LBracket), string(RBracket)))
	self.addRoute(Before, LocalHandler, []string{"GET", "HEAD"}, &TUrl{Path: urlPattern}, []any{handler})
}

// StaticFile registers a single route in order to serve a single file of the local filesystem.
// router.StaticFile("favicon.ico", "./resources/favicon.ico")
func (self *TGroup) StaticFile(relativePath, filepath string) {
	if strings.Contains(relativePath, ":") || strings.Contains(relativePath, "*") {
		panic("URL parameters can not be used when serving a static file")
	}
	handler := func(c *THttpContext) {
		c.ServeFile(filepath)
	}

	self.Url("GET", relativePath, handler)
}

// !NOTE! RPC 或者 HTTP 不适用同一Module注册路由
/*
Add route with method
HTTP: "GET/POST/DELETE/PUT/HEAD/OPTIONS/REST"
RPC: "CONNECT"

Match rules
Base: <type:name> if difine type than the route only match the string same to the type
Example: string:id only match "abc"
         int:id only match number "123"
         :id could match all kind of type with name id
'//' -- 生成"/abc/"而非"/abc"
'/web/content/<string:xmlid>',
'/web/content/<string:xmlid>/<string:filename>',
'/web/content/<int:id>',
'/web/content/<int:id>/<string:filename>',
'/web/content/<int:id>-<string:unique>',
'/web/content/<int:id>-<string:unique>/<string:filename>',
'/web/content/<string:model>/<int:id>/<string:field>',
'/web/content/<string:model>/<int:id>/<string:field>/<string:filename>'
for details please read tree.go
*/
// @handlers 第一个将会被用于主要控制器其他将被归为中间件
// @method:
//			API:会映射所有对象符合要求的控制器作为API发布
//			REST:会映射所有对象符合要求的create, read, update, and delete控制器作为Restful发布
func (self *TGroup) Url(method string, path string, handlers ...any) *route {
	if self.config.PrefixPath != "" && path == "//" {
		// 生成"/abc/"而非"/abc"
		path = _path.Join(self.config.PrefixPath, "") + "/"
	} else {
		path = _path.Join(self.config.PrefixPath, path)
	}

	method = strings.ToUpper(method)
	switch method {
	case "GET":
		return self.addRoute(Normal, LocalHandler, []string{"GET", "HEAD"}, &TUrl{Path: path}, handlers[0:1], handlers[1:]...)
	case "POST", "PUT", "HEAD", "OPTIONS", "TRACE", "PATCH", "DELETE":
		return self.addRoute(Normal, LocalHandler, []string{method}, &TUrl{Path: path}, handlers[0:1], handlers[1:]...)
	case "CONNECT": // RPC or WS
		return self.addRoute(Normal, LocalHandler, []string{method}, &TUrl{Path: path}, handlers[0:1], handlers[1:]...)
	case "REST", "RPC":
		return self.addRoute(Normal, LocalHandler, []string{method}, &TUrl{Path: path}, handlers[0:1], handlers[1:]...)
	default:
		log.Fatalf("the params in Module.Url() %v:%v is invaild", method, path)
	}

	return nil
}

/*
pos: true 为插入Before 反之After
*/
func (self *TGroup) addRoute(position RoutePosition, hanadlerType HandlerType, methods []string, url *TUrl, handlers []any, mids ...any) *route {
	// check vaild
	if hanadlerType != ProxyHandler && len(handlers) == 0 {
		panic("the route must binding a controller!")
	}

	var hd *handler
	h := handlers[0]
	switch v := h.(type) {
	case func(*TRpcContext):
		hd = generateHandler(hanadlerType, RpcHandler, handlers, mids, nil)
	case func(*THttpContext):
		hd = generateHandler(hanadlerType, HttpHandler, handlers, mids, nil)
	case func(http.ResponseWriter, *http.Request):
		hd = generateHandler(hanadlerType, HttpHandler, []interface{}{WrapFn(v)}, mids, nil)
	case http.HandlerFunc:
		hd = generateHandler(hanadlerType, HttpHandler, []interface{}{WrapFn(v)}, mids, nil)
	case http.Handler:
		hd = generateHandler(hanadlerType, HttpHandler, []interface{}{WrapHd(v)}, mids, nil)
	default:
		// init Value and Type
		ctrlValue, ok := h.(reflect.Value)
		if !ok {
			ctrlValue = reflect.ValueOf(h)
		}
		ctrlType := ctrlValue.Type()

		kind := ctrlType.Kind()
		switch kind {
		case reflect.Struct, reflect.Ptr:
			// transfer prt to struct
			if kind == reflect.Ptr {
				ctrlValue = ctrlValue.Elem()
				ctrlType = ctrlType.Elem()
			}

			objName := utils.DotCasedName(utils.Obj2Name(h))
			var name string
			var method reflect.Value
			var contextType reflect.Type
			useREST := utils.InStrings("REST", methods...) > -1
			useRPC := utils.InStrings("RPC", methods...) > -1
			for i := 0; i < ctrlType.NumMethod(); i++ {
				// get the method information from the ctrl Type
				name = ctrlType.Method(i).Name
				method = ctrlType.Method(i).Func

				// 忽略非handler方法
				if method.Type().NumIn() <= 1 {
					continue
				}

				if method.CanInterface() {
					contextType = method.Type().In(1)
					// 添加注册方法
					if useREST && (contextType == HttpContextType || contextType == ContextType) {
						// 方法为对象方法名称 url 只注册对象名称
						// name 为create, read, update, and delete (CRUD)等
						name = NameMapper(name) // 名称格式化
						ul := &TUrl{Path: path.Join(url.Path, name), Controller: objName, Action: name}
						self.addRoute(position, hanadlerType, []string{"GET", "POST"}, ul, []any{method}, mids...)
					}

					if useRPC && (contextType == RpcContextType || contextType == ContextType) {
						name = NameMapper(name) // 名称格式化
						ul := &TUrl{Path: strings.Join([]string{url.Path, name}, "."), Controller: objName, Action: name}
						self.addRoute(position, hanadlerType, []string{"CONNECT"}, ul, []any{method}, mids...)
					}
				}
			}

			// the end of the struct mapping
			return nil //TODO 返回路由
		case reflect.Func:
			// Method must be exported.
			if ctrlType.PkgPath() != "" {
				log.Fatalf("Method %s must be exported", url.Action)
				return nil
			}

			// First arg must be context.Context
			// RPC route validate
			if utils.InStrings("CONNECT", methods...) > -1 {
				ctxType := ctrlType.In(1)
				if ctxType != RpcContextType && ctxType != ContextType {
					log.Fatalf("method %s must use context pointer as the first parameter", url.Action)
					return nil
				}
				hd = generateHandler(hanadlerType, RpcHandler, []interface{}{ctrlValue}, mids, nil)
			} else {
				hd = generateHandler(hanadlerType, HttpHandler, []interface{}{ctrlValue}, mids, nil)
			}

		default:
			log.Fatal("controller must be func or bound method")
		}
	}

	// trim the Url to including "/" on begin of path
	if !strings.HasPrefix(url.Path, "/") && url.Path != "/" {
		url.Path = "/" + url.Path
	}

	route := newRoute(self,
		methods,
		url,
		url.Path,
		self.config.FilePath,
		self.config.Name,
		url.Action,
	)

	route.handlers = append(route.handlers, hd)
	// register route
	self.tree.AddRoute(route)
	return route
}
