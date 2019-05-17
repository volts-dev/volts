package server

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/volts-dev/volts"

	httpx "github.com/volts-dev/volts/server/listener/http"

	"github.com/volts-dev/logger"

	"github.com/volts-dev/template"
	"github.com/volts-dev/utils"
)

/*
	Handler 负责处理控制器Request,Response的数据处理和管理

*/

const (
	HANDLER_VER = "1.2.0"
)

var (
	WebHandlerType       = reflect.TypeOf(TWebHandler{})
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
	// 用来提供API格式化Body
	TContentBody struct {
		handler *TWebHandler
		data    []byte //#**** 由于读完Body被清空所以必须再次保存样本
	}

	TParamsSet struct {
		handler *TWebHandler
		params  map[string]string
		name    string
	}

	// TWebHandler 负责所有请求任务,每个Handle表示有一个请求
	TWebHandler struct {
		volts.ILogger
		httpx.IResponseWriter
		response httpx.IResponseWriter //http.ResponseWriter
		request  *http.Request         //

		Router       *TRouter
		Route        *TRoute                //执行本次Handle的Route
		Template     *template.TTemplateSet // 概念改进  为Hd添加 hd.Template.Params.Set("模板数据",Val)/Get()/Del()
		methodParams *TParamsSet            //map[string]string // Post Get 传递回来的参数
		pathParams   *TParamsSet            //map[string]string // Url 传递回来的参数
		body         *TContentBody

		// 模板
		TemplateSrc string                 // 模板名称
		templateVar map[string]interface{} // TODO (name TemplateData) Args passed to the template.

		// 返回
		ContentType string
		_Data       map[string]interface{} // 数据缓存在各个Controler间调用
		Result      []byte                 // 最终返回数据由Apply提交

		ControllerIndex int // -- 提示目前控制器Index
		//CtrlCount int           // --
		isDone    bool // -- 已经提交过
		finalCall func(handler *TWebHandler)
		//finalCall reflect.Value // -- handler 结束执行的动作处理器
		val reflect.Value
		typ reflect.Type
	}

	// 反向代理
	TProxyHandler struct {
		httpx.IResponseWriter
		Response httpx.IResponseWriter //http.ResponseWriter
		Request  *http.Request         //

		Router *TRouter
		Route  *TRoute //执行本次Handle的Route

		//Logger *logger.TLogger

		// Director must be a function which modifies
		// the request into a new request to be sent
		// using Transport. Its response is then copied
		// back to the original client unmodified.
		Director func(*http.Request)

		// The transport used to perform proxy requests.
		// If nil, http.DefaultTransport is used.
		Transport http.RoundTripper

		// FlushInterval specifies the flush interval
		// to flush to the client while copying the
		// response body.
		// If zero, no periodic flushing is done.
		FlushInterval time.Duration
		// BufferPool optionally specifies a buffer pool to
		// get byte slices for use by io.CopyBuffer when
		// copying HTTP response bodies.
		BufferPool BufferPool
		// ModifyResponse is an optional function that
		// modifies the Response from the backend.
		// If it returns an error, the proxy returns a StatusBadGateway error.
		ModifyResponse func(*http.Response) error
	}
)

// 100%返回TContentBody
func NewContentBody(hd *TWebHandler) (res *TContentBody) {
	res = &TContentBody{
		handler: hd,
	}
	body, err := ioutil.ReadAll(hd.request.Body)
	if err != nil {
		logger.Err("Read request body faild with an error : %s!", err.Error())
	}

	res.data = body
	return
}

func NewParamsSet(hd *TWebHandler) *TParamsSet {
	return &TParamsSet{
		handler: hd,
		params:  make(map[string]string),
	}
}

func NewWebHandler(router *TRouter) *TWebHandler {
	hd := &TWebHandler{
		ILogger: router.server.logger,
		Router:  router,
		//Route:   route,
		//iResponseWriter: writer,
		//Response:        writer,
		//Request:         request,
		//MethodParams: map[string]string{},
		//PathParams:   map[string]string{},
		templateVar: make(map[string]interface{}),
		//Data:       make(map[string]interface{}),
	} // 这个handle将传递给 请求函数的头一个参数func test(hd *webgo.TWebHandler) {}

	// 必须不为nil
	//	hd.MethodParams=NewParamsSet(hd)
	hd.pathParams = NewParamsSet(hd)
	hd.val = reflect.ValueOf(hd)
	hd.typ = hd.val.Type()
	return hd
}

func NewProxyHandler() *TProxyHandler {
	hd := &TProxyHandler{}
	return hd
}

func (self *TContentBody) AsBytes() []byte {
	return self.data
}

// Body 必须是Json结构才能你转
func (self *TContentBody) AsMap() (map[string]interface{}, error) {
	result := make(map[string]interface{})
	err := json.Unmarshal(self.data, &result)
	if err != nil {
		logger.Err(err.Error())
		return nil, err
	}

	return result, err
}

func (self *TParamsSet) AsString(name string) string {
	return self.params[name]
}

func (self *TParamsSet) AsInteger(name string) int64 {
	return utils.StrToInt64(self.params[name])
}

func (self *TParamsSet) AsBoolean(name string) bool {
	return utils.StrToBool(self.params[name])
}

func (self *TParamsSet) AsDateTime(name string) (t time.Time) {
	t, _ = time.Parse(time.RFC3339, self.params[name])
	return
}

func (self *TParamsSet) AsFloat(name string) float64 {
	return utils.StrToFloat(self.params[name])
}

// Call in the end of all controller
func (self *TWebHandler) FinalCall(handler func(*TWebHandler)) {
	self.finalCall = handler // reflect.ValueOf(handler)
}

/*
func (self *TWebHandler) getPathParams() {
	// 获得正则字符做为Handler参数
	lSubmatch := self.Route.regexp.FindStringSubmatch(self.Request.URL.Path) //更加深层次正则表达式比对nil 为没有
	if lSubmatch != nil && lSubmatch[0] == self.Request.URL.Path {           // 第一个是Match字符串本身
		for i, arg := range lSubmatch[1:] { ///Url正则字符作为参数
			if arg != "" {
				self.PathParams[self.Route.regexp.SubexpNames()[i+1]] = arg //SubexpNames 获得URL 上的(?P<keywords>.*)
			}
		}
	}
}
*/

func (self *TWebHandler) Request() *http.Request {
	return self.request
}

func (self *TWebHandler) Response() httpx.IResponseWriter {
	return self.response
}

func (self *TWebHandler) IsDone() bool {
	return self.isDone
}

// the reflect model of Value
func (self *TWebHandler) ValueModel() reflect.Value {
	return self.val
}

// the reflect model of Type
func (self *TWebHandler) TypeModel() reflect.Type {
	return self.typ
}

//TODO 添加验证Request 防止多次解析
func (self *TWebHandler) MethodParams() *TParamsSet {
	if self.methodParams == nil {
		self.methodParams = NewParamsSet(self)

		// pares the date of GET
		q := self.request.URL.Query()
		for key, _ := range q {
			//Debug("key:", key)
			self.methodParams.params[key] = q.Get(key)
		}

		// pares the date of POST
		ct := self.request.Header.Get("Content-Type")
		ct, _, _ = mime.ParseMediaType(ct)
		if ct == "multipart/form-data" {
			self.request.ParseMultipartForm(256)
		} else {
			self.request.ParseForm() //#Go通过r.ParseForm之后，把用户POST和GET的数据全部放在了r.Form里面
		}

		for key, _ := range self.request.Form {
			//Debug("key2:", key)
			self.methodParams.params[key] = self.request.FormValue(key)
		}

	}

	return self.methodParams
}

// 如果返回 nil 代表 Url 不含改属性
func (self *TWebHandler) PathParams() *TParamsSet {
	return self.pathParams
}

func (self *TWebHandler) Body() *TContentBody {
	if self.body == nil {
		self.body = NewContentBody(self)
	}

	//self.Request.Body.Close()
	//self.Request.Body = ioutil.NopCloser(bytes.NewBuffer(body))
	return self.body
}

// 值由Router 赋予
//func (self *TWebHandler) setPathParams(name, val string) {
func (self *TWebHandler) setPathParams(p Params) {
	// init dy url parm to handler
	if len(p) > 0 {
		self.pathParams = NewParamsSet(self)
	}

	for _, param := range p {
		self.pathParams.params[param.Name] = param.Value
	}

}

/*
func (self *TWebHandler) UpdateSession() {
	self.Router.Sessions.UpdateSession(self.COOKIE[self.Router.Sessions.CookieName], self.SESSION)
}
*/

/*
刷新
#刷新Handler的新请求数据
*/
// Inite and Connect a new ResponseWriter when a new request is coming
func (self *TWebHandler) reset(rw IResponse, req IRequest, router *TRouter, route *TRoute) {
	self.pathParams = nil
	self.methodParams = nil
	self.request = req.(*http.Request)
	self.response = rw.(*httpx.TResponseWriter)
	self.IResponseWriter = rw.(*httpx.TResponseWriter)
	self.Route = route
	self.Template = router.template
	self.TemplateSrc = ""
	self.ContentType = ""
	self.templateVar = make(map[string]interface{}) // 清空
	//self.Data = make(map[string]interface{})       // 清空
	self.body = nil
	self.Result = nil
	self.ControllerIndex = 0 // -- 提示目前控制器Index
	//self.CtrlCount = 0     // --
	self.isDone = false // -- 已经提交过
}

// TODO 修改API名称  设置response数据
func (self *TWebHandler) setData(v interface{}) {
	// self.Result = v.([]byte)
}

// apply all changed to data
func (self *TWebHandler) Apply() {
	if !self.isDone {
		if self.TemplateSrc != "" {
			self.SetHeader(true, "Content-Type", self.ContentType)
			//self.Template.Render(self.TemplateSrc, self.response, self.RenderArgs)
			err := self.Template.RenderToWriter(self.TemplateSrc, self.templateVar, self.response)
			if err != nil {
				http.Error(self.response, "Apply fail:"+err.Error(), http.StatusInternalServerError)
			}
		} else if !self.response.Written() { // STEP:只许一次返回
			self.Write(self.Result)
		}

		self.isDone = true
	}

	return
}

func (self *TWebHandler) _CtrlCount() int {
	return len(self.Route.Ctrls)
}

func (self *TWebHandler) GetCookie(name, key string) (val string) {
	ck, err := self.request.Cookie(name)
	if err != nil {
		return
	}

	val, _ = url.QueryUnescape(ck.Value)
	return
}

func (self *TWebHandler) GetModulePath() string {
	return self.Route.FileName
}

func (self *TWebHandler) IP() (res []string) {
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
func (self *TWebHandler) RemoteAddr() string {
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

//SetCookie Sets the header entries associated with key to the single element value. It replaces any existing values associated with key.
//一个cookie  有名称,内容,原始值,域,大小,过期时间,安全
//cookie[0] => name string
//cookie[1] => value string
//cookie[2] => expires string
//cookie[3] => path string
//cookie[4] => domain string
func (self *TWebHandler) SetCookie(name string, value string, others ...interface{}) {
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
func (self *TWebHandler) SetHeader(unique bool, hdr string, val string) {
	if unique {
		self.response.Header().Set(hdr, val)
	} else {
		self.response.Header().Add(hdr, val)
	}
}

func (self *TWebHandler) Abort(status int, body string) {
	self.response.WriteHeader(status)
	self.response.Write([]byte(body))
	self.isDone = true
}

func (self *TWebHandler) Respond(content []byte) {
	self.Result = content
}

func (self *TWebHandler) RespondError(error string) {
	self.Header().Set("Content-Type", "text/plain; charset=utf-8")
	self.Header().Set("X-Content-Type-Options", "nosniff")
	self.WriteHeader(http.StatusInternalServerError)
	fmt.Fprintln(self, error)

	// stop run next ctrl
	self.isDone = true
}

func (self *TWebHandler) NotModified() {
	self.IResponseWriter.WriteHeader(304)
}

// Respond content by Json mode
func (self *TWebHandler) RespondByJson(aBody interface{}) {

	lJson, err := json.Marshal(aBody)
	if err != nil {
		self.response.Write([]byte(err.Error()))
		return
	}

	self.response.Header().Set("Content-Type", "application/json; charset=UTF-8")
	self.Result = lJson
}

// Ck
func (self *TWebHandler) Redirect(urlStr string, status ...int) {
	//http.Redirect(self, self.request, urlStr, code)
	lStatusCode := http.StatusFound
	if len(status) > 0 {
		lStatusCode = status[0]
	}

	self.Header().Set("Location", urlStr)
	self.WriteHeader(lStatusCode)
	//self.Write([]byte("Redirecting to: " + urlStr))
	self.Result = []byte("Redirecting to: " + urlStr)

	// stop run next ctrl
	//self.isDone = true
}

// return a download file redirection for client
func (self *TWebHandler) Download(file_path string) error {
	f, err := os.Open(file_path)
	if err != nil {
		return err
	}
	defer f.Close()

	fName := filepath.Base(file_path)
	self.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%v\"", fName))
	_, err = io.Copy(self, f)
	return err
}

func (self *TWebHandler) ServeFile(file_path string) {
	http.ServeFile(self.response, self.request, file_path)
}

// render the template and return to the end
func (self *TWebHandler) RenderTemplate(tmpl string, args interface{}) {
	self.ContentType = "text/html; charset=utf-8"
	if vars, ok := args.(map[string]interface{}); ok {
		self.templateVar = utils.MergeMaps(self.Router.templateVar, vars) // 添加Router的全局变量到Templete
	} else {
		self.templateVar = self.Router.templateVar // 添加Router的全局变量到Templete
	}

	if self.Route.FilePath == "" {
		self.TemplateSrc = filepath.Join(TEMPLATE_DIR, tmpl)
	} else {
		self.TemplateSrc = filepath.Join(MODULE_DIR, self.Route.FilePath, TEMPLATE_DIR, tmpl)
		//self.TemplateSrc = filepath.Join(self.Route.FilePath,TEMPLATE_DIR, tmpl)

	}
}

// remove the var from the template
func (self *TWebHandler) DelTemplateVar(key string) {
	delete(self.templateVar, key)
}

func (self *TWebHandler) GetTemplateVar() map[string]interface{} {
	return self.templateVar
}

// set the var of the template
func (self *TWebHandler) SetTemplateVar(key string, value interface{}) {
	self.templateVar[key] = value
}

// Responds with 404 Not Found
func (self *TWebHandler) RespondWithNotFound(message ...string) {
	//self.Abort(http.StatusNotFound, body)
	if len(message) == 0 {
		self.Abort(http.StatusNotFound, http.StatusText(http.StatusNotFound))
		return
	}
	self.Abort(http.StatusNotFound, message[0])
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

func (self *TProxyHandler) connect(rw httpx.IResponseWriter, req *http.Request, Router *TRouter, Route *TRoute) {
	self.Request = req
	self.Response = rw
	self.IResponseWriter = rw
	self.Router = Router
	self.Route = Route
	//self.Logger = Router.Server.Logger

	director := func(req *http.Request) {
		target := self.Route.Host
		targetQuery := target.RawQuery
		req.URL.Scheme = self.Route.Host.Scheme
		req.URL.Host = self.Route.Host.Host
		req.URL.Path = singleJoiningSlash(target.Path, req.URL.Path)
		if targetQuery == "" || req.URL.RawQuery == "" {
			req.URL.RawQuery = targetQuery + req.URL.RawQuery
		} else {
			req.URL.RawQuery = targetQuery + "&" + req.URL.RawQuery
		}
		if _, ok := req.Header["User-Agent"]; !ok {
			// explicitly disable User-Agent so it's not set to default value
			req.Header.Set("User-Agent", "")
		}
	}

	if Route.Host.Scheme == "http" {
		self.Director = director
		self.Transport = http.DefaultTransport

	} else {
		self.Director = func(req *http.Request) {
			director(req)
			req.Host = req.URL.Host
		}

		// Set a custom DialTLS to access the TLS connection state
		self.Transport = &http.Transport{
			DialTLS: func(network, addr string) (net.Conn, error) {
				conn, err := net.Dial(network, addr)
				if err != nil {
					return nil, err
				}

				host, _, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				cfg := &tls.Config{ServerName: host}

				tlsConn := tls.Client(conn, cfg)
				if err := tlsConn.Handshake(); err != nil {
					conn.Close()
					return nil, err
				}

				cs := tlsConn.ConnectionState()
				cert := cs.PeerCertificates[0]

				// Verify here
				cert.VerifyHostname(host)
				//self.Logger.Dbg(cert.Subject)

				return tlsConn, nil
			}}
	}
}

func (p *TProxyHandler) copyBuffer(dst io.Writer, src io.Reader, buf []byte) (int64, error) {
	if len(buf) == 0 {
		buf = make([]byte, 32*1024)
	}
	var written int64
	for {
		nr, rerr := src.Read(buf)
		if rerr != nil && rerr != io.EOF {
			logger.Err("httputil: ReverseProxy read error during body copy: %v", rerr)
		}
		if nr > 0 {
			nw, werr := dst.Write(buf[:nr])
			if nw > 0 {
				written += int64(nw)
			}
			if werr != nil {
				return written, werr
			}
			if nr != nw {
				return written, io.ErrShortWrite
			}
		}
		if rerr != nil {
			return written, rerr
		}
	}
}

func (p *TProxyHandler) copyResponse(dst io.Writer, src io.Reader) {
	if p.FlushInterval != 0 {
		if wf, ok := dst.(writeFlusher); ok {
			mlw := &maxLatencyWriter{
				dst:     wf,
				latency: p.FlushInterval,
				done:    make(chan bool),
			}
			go mlw.flushLoop()
			defer mlw.stop()
			dst = mlw
		}
	}

	var buf []byte
	if p.BufferPool != nil {
		buf = p.BufferPool.Get()
	}
	p.copyBuffer(dst, src, buf)
	if p.BufferPool != nil {
		p.BufferPool.Put(buf)
	}
}

/*
func (self *TRestHandle) RespondByJson(src interface{}) {
	//var s []map[string]string
	//rawValue := reflect.Indirect(reflect.ValueOf(src))
	//s = rawValue.Interface().([]map[string]string)
	runtime.ReadMemStats(memstats)
	fmt.Printf("MemStats=%d|%d|%d|%d|RespondByJson", memstats.Mallocs, memstats.Sys, memstats.Frees, memstats.HeapObjects)

	s1, _ := json.Marshal(src)
	//str := string(s)
	//fmt.Println(s1)
	runtime.ReadMemStats(memstats)
	fmt.Printf("MemStats=%d|%d|%d|%d|RespondByJson", memstats.Mallocs, memstats.Sys, memstats.Frees, memstats.HeapObjects)

	self.Write(s1)
	runtime.ReadMemStats(memstats)
	fmt.Printf("MemStats=%d|%d|%d|%d|RespondByJson", memstats.Mallocs, memstats.Sys, memstats.Frees, memstats.HeapObjects)

}
*/
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
