package client

import (
	"net/http"
	"net/url"

	"github.com/volts-dev/volts/internal/body"
	"github.com/volts-dev/volts/internal/header"
)

type httpResponse struct {
	response *http.Response
	_header  map[string]string
	body     *body.TBody

	Status string // e.g. "200 OK"
	Code   int    // HTTP status code, e.g. 200
}

// TODO 修改到结构里
type HttpResponse = httpResponse

// Body 返回响应正文。
//
// 正文在构造这个对象**之前**就已经被整份读进来了（见 HttpClient.call：先
// `io.ReadAll(hrsp.Body)`，再 `body.New(...)`、`bd.Data.Write(b)`，然后才建
// httpResponse），所以这里不再尝试从 `self.response.Body` 补读 —— 那个 reader 此刻
// 已经被 `defer hrsp.Body.Close()` 关掉，补读只可能拿到 0 字节加一句
// "read on closed response body"。
//
// 那段补读的错误判断还是**反的**：`if err == nil { log.Err(err) }` —— 读成功时打印
// 一个 nil，真出错时反倒一声不响，并且把残缺内容照写不误。既然整段本就取不到东西，
// 直接去掉，而不是把条件改过来（改过来只会让每个空正文的响应刷一条错误日志）。
func (self *httpResponse) Body() *body.TBody {
	return self.body
}

func (self *httpResponse) Request() *http.Request {
	return self.response.Request
}

func (self *httpResponse) Cookies() []*http.Cookie {
	return self.response.Cookies()
}

func (self *httpResponse) Header() header.Header {
	return header.Header(self.response.Header)
}

func (self *httpResponse) Location() (*url.URL, error) {
	return self.response.Location()
}

// Read 按正文的编解码器把响应解进 out。
//
// 此前这个方法的函数体就是 `return nil` —— 调用方拿到一个原封不动的零值和一个 nil
// 错误，看起来一切正常。它不在 IResponse 接口里、本仓也没有调用方，但 `HttpResponse`
// 是导出别名，外部拿得到它，而方法名承诺的正是"把响应读出来"。
//
// 用的是**请求那一侧**的编解码器（构造时 `body.New(req.body.Codec)`）—— 对绝大多数
// 「发 JSON 收 JSON」的调用是对的。对端换了格式时请改用
// `rsp.Body().AsMap(codecType)` 显式指定。
func (self *httpResponse) Read(out interface{}) error {
	return self.body.Decode(out)
}

func (self *httpResponse) StatusCode() int {
	return self.Code
}
