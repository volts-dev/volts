package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/volts-dev/template"
	"github.com/volts-dev/utils"
	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/transport"
	"github.com/volts-dev/volts/util/body"
	//httpx "github.com/volts-dev/volts/server/listener/http"
)

/*
	Handler 负责处理控制器Request,Response的数据处理和管理

*/

var (
	HttpContext          = "HttpContext"                   // 标识用于判断String()
	HttpContextType      = reflect.TypeOf(&THttpContext{}) // must be a pointer
	cookieNameSanitizer  = strings.NewReplacer("\n", "-", "\r", "-")
	cookieValueSanitizer = strings.NewReplacer("\n", " ", "\r", " ", ";", " ")

	// onExitFlushLoop is a callback set by tests to detect the state of the
	// flushLoop() goroutine.
	onExitFlushLoop func()
	hopHeaders      = []string{
		"Connection",
		"Proxy-Connection", // non-standard but still sent by libcurl and rejected by e.g. google
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",      // canonicalized version of "TE"
		"Trailer", // not Trailers per URL above; http://www.rfc-editor.org/errata_search.php?eid=4522
		"Transfer-Encoding",
		"Upgrade",
	}
)

// A BufferPool is an interface for getting and returning temporary
// byte slices for use by io.CopyBuffer.
type BufferPool interface {
	Get() []byte
	Put([]byte)
}

type writeFlusher interface {
	io.Writer
	http.Flusher
}

type maxLatencyWriter struct {
	dst     writeFlusher
	latency time.Duration

	mu   sync.Mutex // protects Write + Flush
	done chan bool
}
type (
	// THttpContext 负责所有请求任务,每个Handle表示有一个请求
	THttpContext struct {
		logger.ILogger
		http.ResponseWriter
		context  context.Context
		response *transport.THttpResponse //http.ResponseWriter
		request  *transport.THttpRequest  //
		router   *TRouter
		route    route //执行本次Handle的Route

		// data set
		data         *TParamsSet // 数据缓存在各个Controler间调用
		methodParams *TParamsSet //map[string]string // Post Get 传递回来的参数
		pathParams   *TParamsSet //map[string]string // Url 传递回来的参数
		//body         *TContentBody

		// 模板
		TemplateVar
		Template    *template.TTemplateSet // 概念改进  为Hd添加 hd.Template.Params.Set("模板数据",Val)/Get()/Del()
		TemplateSrc string                 // 模板名称

		// 返回
		ContentType  string
		Result       []byte   // -- 最终返回数据由Apply提交
		handler      *handler // -- 当前处理器
		handlerIndex int      // -- 提示目前处理器索引
		isDone       bool     // -- 已经提交过
		inited       bool     // -- 初始化固定值保存进POOL
		val          reflect.Value
		typ          reflect.Type
	}
)

func NewHttpContext(router *TRouter) *THttpContext {
	hd := &THttpContext{
		ILogger: log,
		router:  router,
		//Route:   route,
		//iResponseWriter: writer,
		//Response:        writer,
		//Request:         request,
		//MethodParams: map[string]string{},
		//PathParams:   map[string]string{},
		//Data:       make(map[string]interface{}),
	} // 这个handle将传递给 请求函数的头一个参数func test(hd *webgo.THttpContext) {}

	// 必须不为nil
	//	hd.MethodParams=NewParamsSet(hd)
	hd.pathParams = NewParamsSet(hd)
	hd.data = NewParamsSet(hd)
	hd.val = reflect.ValueOf(hd)
	hd.typ = hd.val.Type()
	return hd
}

func (self *THttpContext) Router() IRouter {
	return self.router
}

func (self *THttpContext) Request() *transport.THttpRequest {
	return self.request
}

func (self *THttpContext) Response() *transport.THttpResponse {
	return self.response
}

func (self *THttpContext) IsDone() bool {
	return self.isDone
}

// the reflect model of Value
func (self *THttpContext) ValueModel() reflect.Value {
	return self.val
}

// the reflect model of Type
func (self *THttpContext) TypeModel() reflect.Type {
	return self.typ
}
func (self *THttpContext) Next() {
	self.handler.Invoke(self)
}

func (self *THttpContext) setHandler(h *handler) {
	self.handler = h
}

// TODO 添加验证Request 防止多次解析
func (self *THttpContext) MethodParams(blank ...bool) *TParamsSet {
	var useBlank bool
	if len(blank) > 0 {
		useBlank = blank[0]
	}

	if self.methodParams == nil {
		self.methodParams = NewParamsSet(self)

		if !useBlank && self.methodParams.Length() == 0 {
			// # parse the data from GET method #
			q := self.request.URL.Query()
			for key := range q {
				self.methodParams.FieldByName(key).AsInterface(q.Get(key))
			}

			// # parse the data from POST method #
			var err error
			ct := self.request.Header.Get("Content-Type")
			ct, _, err = mime.ParseMediaType(ct)
			if err != nil {
				logger.Err(err)
				return self.methodParams
			} else {
				if ct == "multipart/form-data" {
					self.request.ParseMultipartForm(256)
				} else {
					self.request.ParseForm() //#Go通过r.ParseForm之后，把用户POST和GET的数据全部放在了r.Form里面
				}

				for key := range self.request.Form {
					//Debug("key2:", key)
					self.methodParams.FieldByName(key).AsInterface(self.request.FormValue(key))
				}
			}
		}
	}

	return self.methodParams
}

func (self *THttpContext) Body() *body.TBody {
	return self.request.Body()
}

func (self *THttpContext) Write(data []byte) (int, error) {
	return self.response.Write(data)
}

func (self *THttpContext) WriteStream(data interface{}) error {
	switch v := data.(type) {
	case []byte:
		_, err := self.response.Write(v)
		return err
	case string:
		_, err := self.response.Write([]byte(v))
		return err
	}

	return errors.New("only accept []byte type data")
}

// 如果返回 nil 代表 Url 不含改属性
func (self *THttpContext) PathParams() *TParamsSet {
	return self.pathParams
}

func (self *THttpContext) String() string {
	return HttpContext
}

// 值由Router 赋予
// func (self *THttpContext) setPathParams(name, val string) {
func (self *THttpContext) setPathParams(p Params) {
	// init dy url parm to handler
	if len(p) > 0 {
		self.pathParams = NewParamsSet(self)
	}

	for _, param := range p {
		self.pathParams.FieldByName(param.Name).AsInterface(param.Value)
	}
}

/*
func (self *THttpContext) UpdateSession() {
	self.Router.Sessions.UpdateSession(self.COOKIE[self.Router.Sessions.CookieName], self.SESSION)
}
*/

/*
刷新
#刷新Handler的新请求数据
*/
// Inite and Connect a new ResponseWriter when a new request is coming
func (self *THttpContext) reset(rw *transport.THttpResponse, req *transport.THttpRequest) {
	self.TemplateVar = *newTemplateVar() // 清空
	self.data = nil                      // 清空
	self.pathParams = nil
	self.methodParams = nil
	self.request = req
	self.response = rw
	self.ResponseWriter = rw
	self.TemplateSrc = ""
	self.ContentType = ""
	self.Result = nil
	self.handlerIndex = 0 // -- 提示目前控制器Index
	self.isDone = false   // -- 已经提交过
}

// TODO 修改API名称  设置response数据
func (self *THttpContext) setData(v interface{}) {
	// self.Result = v.([]byte)
}

func (self *THttpContext) setControllerIndex(num int) {
	self.handlerIndex = num
}

func (self *THttpContext) HandlerIndex() int {
	return self.handlerIndex
}

func (self *THttpContext) Context() context.Context {
	return self.context
}

// apply all changed to data
func (self *THttpContext) Apply() {
	if !self.isDone {
		if self.TemplateSrc != "" {
			self.SetHeader(true, "Content-Type", self.ContentType)
			if err := self.Template.RenderToWriter(self.TemplateSrc, self.templateVar, self.response); err != nil {
				http.Error(self.response, "Apply fail:"+err.Error(), http.StatusInternalServerError)
			}
		} else if !self.response.Written() { // STEP:只许一次返回
			self.Write(self.Result)
		}

		self.isDone = true
	}

	return
}

func (self *THttpContext) Route() route {
	return self.route
}

func (self *THttpContext) Handler(index ...int) *handler {
	idx := self.handlerIndex
	if len(index) > 0 {
		idx = index[0]
	}

	if idx == self.handlerIndex {
		return self.handler
	}

	return self.route.handlers[idx]
}

func (self *THttpContext) _CtrlCount() int {
	return len(self.route.handlers)
}

// 数据缓存供传递用
func (self *THttpContext) Data() *TParamsSet {
	if self.data == nil {
		self.data = NewParamsSet(self)
	}

	return self.data
}

func (self *THttpContext) GetCookie(name, key string) (value string, err error) {
	ck, err := self.request.Cookie(name)
	if err != nil {
		return "", err
	}

	return url.QueryUnescape(ck.Value)
}

func (self *THttpContext) ___GetGroupPath() string {
	//return self.route.FileName
	return ""
}

func (self *THttpContext) IP() (res []string) {
	ip := strings.Split(self.request.RemoteAddr, ":")
	if len(ip) > 0 {
		if ip[0] != "[" {
			res = append(res, ip[0])
		}
	}

	proxy := make([]string, 0)
	if ips := self.request.Header.Get("X-Forwarded-For"); ips != "" {
		proxy = strings.Split(ips, ",")
	}
	if len(proxy) > 0 && proxy[0] != "" {
		res = append(res, proxy[0])
	}

	if len(res) == 0 {
		res = append(res, "127.0.0.1")
	}
	return
}

// RemoteAddr returns more real IP address of visitor.
func (self *THttpContext) RemoteAddr() string {
	addr := self.request.Header.Get("X-Real-IP")
	if len(addr) == 0 {
		addr = self.request.Header.Get("X-Forwarded-For")
		if addr == "" {
			addr = self.request.RemoteAddr
			if i := strings.LastIndex(addr, ":"); i > -1 {
				addr = addr[:i]
			}
		}
	}
	return addr
}

// SetCookie Sets the header entries associated with key to the single element value. It replaces any existing values associated with key.
// 一个cookie  有名称,内容,原始值,域,大小,过期时间,安全
// cookie[0] => name string
// cookie[1] => value string
// cookie[2] => expires string
// cookie[3] => path string
// cookie[4] => domain string
func (self *THttpContext) SetCookie(name string, value string, others ...interface{}) {
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s=%s", sanitizeCookieName(name), sanitizeCookieValue(value))

	if len(others) > 0 {
		switch others[0].(type) {
		case int:
			if others[0].(int) > 0 {
				fmt.Fprintf(&b, "; Max-Age=%d", others[0].(int))
			} else if others[0].(int) < 0 {
				fmt.Fprintf(&b, "; Max-Age=0")
			}
		case int64:
			if others[0].(int64) > 0 {
				fmt.Fprintf(&b, "; Max-Age=%d", others[0].(int64))
			} else if others[0].(int64) < 0 {
				fmt.Fprintf(&b, "; Max-Age=0")
			}
		case int32:
			if others[0].(int32) > 0 {
				fmt.Fprintf(&b, "; Max-Age=%d", others[0].(int32))
			} else if others[0].(int32) < 0 {
				fmt.Fprintf(&b, "; Max-Age=0")
			}
		}
	}
	if len(others) > 1 {
		fmt.Fprintf(&b, "; Path=%s", sanitizeCookieValue(others[1].(string)))
	}
	if len(others) > 2 {
		fmt.Fprintf(&b, "; Domain=%s", sanitizeCookieValue(others[2].(string)))
	}
	if len(others) > 3 {
		if others[3].(bool) {
			fmt.Fprintf(&b, "; Secure")
		}
	}

	if len(others) > 4 {
		if others[4].(bool) {
			fmt.Fprintf(&b, "; HttpOnly")
		}
	}
	self.response.Header().Add("Set-Cookie", b.String())
	/*
		if aName == "" && aValue == "" { // 不能少于两个参数
			return
		}
		fmt.Println(args, len(args))
		var (
			//name    string
			//value   string
			expires int
			path    string
			domain  string
		)
		if len(args) > 0 {
			if v, ok := args[0].(int); ok {
				expires = v
			}
		}
		if len(args) > 1 {
			if v, ok := args[1].(string); ok {
				path = v
			}
		}
		if len(args) > 2 {
			if v, ok := args[2].(string); ok {
				domain = v
			}
		}

		lpCookie := &http.Cookie{
			Name:   aName,
			Value:  url.QueryEscape(aValue),
			Path:   path,
			Domain: domain,
		}

		if expires > 0 { //设置过期时间
			d, _ := time.ParseDuration(strconv.Itoa(expires) + "s")
			lpCookie.Expires = time.Now().Add(d)
		}
		if unique {
			self.response.Header().Set("Set-Cookie", lpCookie.String()) // 等同http.SetCookie()

		} else {
			self.response.Header().Add("Set-Cookie", lpCookie.String()) // 等同http.SetCookie()

		}
	*/
	/*
		if expires > 0 {
			p.COOKIE[pCookie.Name] = pCookie.Value
		} else {
			delete(p.COOKIE, pCookie.Name)
		}
	*/
}

// set the header of response
func (self *THttpContext) SetHeader(unique bool, hdr string, val string) {
	if unique {
		self.response.Header().Set(hdr, val)
	} else {
		self.response.Header().Add(hdr, val)
	}
}

func (self *THttpContext) Abort(body string, code ...int) {
	if len(code) > 0 {
		self.response.WriteHeader(code[0])
	} else {
		self.response.WriteHeader(http.StatusInternalServerError)
	}
	self.response.Write([]byte(body))
	self.isDone = true
}

func (self *THttpContext) Respond(content []byte) {
	self.Result = content
}

func (self *THttpContext) RespondError(error string) {
	self.Header().Set("Content-Type", "text/plain; charset=utf-8")
	self.Header().Set("X-Content-Type-Options", "nosniff")
	self.WriteHeader(http.StatusInternalServerError)
	fmt.Fprintln(self.response, error)

	// stop run next ctrl
	self.isDone = true
}

func (self *THttpContext) NotModified() {
	self.response.WriteHeader(304)
}

// NOTE default EscapeHTML=false
// Respond content by Json mode
func (self *THttpContext) RespondByJson(data interface{}) {
	buf := bytes.NewBuffer([]byte{})
	js := json.NewEncoder(buf)
	js.SetEscapeHTML(false)
	if err := js.Encode(data); err != nil {
		self.response.Write([]byte(err.Error()))
		return
	}

	self.response.Header().Set("Content-Type", "application/json; charset=UTF-8")
	self.Result = buf.Bytes()
}

// Ck
func (self *THttpContext) Redirect(urlStr string, status ...int) {
	//http.Redirect(self, self.request, urlStr, code)
	lStatusCode := http.StatusFound
	if len(status) > 0 {
		lStatusCode = status[0]
	}

	self.Header().Set("Location", urlStr)
	self.WriteHeader(lStatusCode)
	self.Result = []byte("Redirecting to: " + urlStr)

	// stop run next ctrl
	self.isDone = true
}

// return a download file redirection for client
func (self *THttpContext) Download(file_path string) error {
	f, err := os.Open(file_path)
	if err != nil {
		return err
	}
	defer f.Close()

	fName := filepath.Base(file_path)
	self.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%v\"", fName))
	_, err = io.Copy(self.response, f)
	return err
}

func (self *THttpContext) ServeFile(file_path string) {
	http.ServeFile(self.response, self.request.Request, file_path)
}

// TODO 自定义文件夹
// render the template and return to the end
func (self *THttpContext) RenderTemplate(tmpl string, args interface{}) {
	self.ContentType = "text/html; charset=utf-8"
	if vars, ok := args.(map[string]interface{}); ok {
		self.templateVar = utils.MergeMaps(self.router.templateVar, self.route.group.templateVar, vars) // 添加Router的全局变量到Templete
	} else {
		self.templateVar = utils.MergeMaps(self.router.templateVar, self.route.group.templateVar) // 添加Router的全局变量到Templete
	}

	if self.route.FilePath == "" {
		self.TemplateSrc = filepath.Join(TEMPLATE_DIR, tmpl)
	} else {
		self.TemplateSrc = filepath.Join(self.route.FilePath, TEMPLATE_DIR, tmpl)
	}
}

// remove the var from the template
func (self *THttpContext) DelTemplateVar(key string) {
	delete(self.templateVar, key)
}

func (self *THttpContext) GetTemplateVar() map[string]interface{} {
	return self.templateVar
}

// set the var of the template
func (self *THttpContext) SetTemplateVar(key string, value interface{}) {
	self.templateVar[key] = value
}

// Responds with 404 Not Found
func (self *THttpContext) NotFound(message ...string) {
	self.isDone = true
	self.response.WriteHeader(http.StatusNotFound)
	if len(message) > 0 {
		self.response.Write([]byte(message[0]))
		return
	}
	self.response.Write([]byte(http.StatusText(http.StatusNotFound)))
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}

func sanitizeCookieName(n string) string {
	return cookieNameSanitizer.Replace(n)
}

func sanitizeCookieValue(v string) string {
	return cookieValueSanitizer.Replace(v)
	//return sanitizeOrWarn("Cookie.Value", validCookieValueByte, v)
}

func sanitizeOrWarn(fieldName string, valid func(byte) bool, v string) string {
	ok := true
	for i := 0; i < len(v); i++ {
		if valid(v[i]) {
			continue
		}
		fmt.Printf("net/http: invalid byte %q in %s; dropping invalid bytes", v[i], fieldName)
		ok = false
		break
	}
	if ok {
		return v
	}
	buf := make([]byte, 0, len(v))
	for i := 0; i < len(v); i++ {
		if b := v[i]; valid(b) {
			buf = append(buf, b)
		}
	}
	return string(buf)
}

func validCookieValueByte(b byte) bool {
	return 0x20 < b && b < 0x7f && b != '"' && b != ',' && b != ';' && b != '\\'
}

func (m *maxLatencyWriter) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dst.Write(p)
}

func (m *maxLatencyWriter) flushLoop() {
	t := time.NewTicker(m.latency)
	defer t.Stop()
	for {
		select {
		case <-m.done:
			if onExitFlushLoop != nil {
				onExitFlushLoop()
			}
			return
		case <-t.C:
			m.mu.Lock()
			m.dst.Flush()
			m.mu.Unlock()
		}
	}
}

func (m *maxLatencyWriter) stop() { m.done <- true }
