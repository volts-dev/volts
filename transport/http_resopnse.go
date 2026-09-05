package transport

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"reflect"

	"github.com/volts-dev/volts/internal/body"
)

// 参考Tango
type (
	IResponseWriter interface {
		http.ResponseWriter
		// Status returns the status code of the response or 0 if the response has not been written.
		Status() int
		// Written returns whether or not the ResponseWriter has been written.
		Written() bool
		// Size returns the size of the response body.
		Size() int
	}

	THttpResponse struct {
		body *body.TBody //
		http.ResponseWriter
		status    int  // 记下的状态码；0 = 还没人决定过这条响应
		committed bool // 状态行与头是否已经发到连接上（发出即冻结）
		size      int

		Val reflect.Value
	}
)

func NewHttpResponse(ctx context.Context, req *THttpRequest) *THttpResponse {
	resp := &THttpResponse{
		body: body.New(req.Codec()),
	}
	resp.Val = reflect.ValueOf(resp)
	return resp
}

func (self *THttpResponse) Body() *body.TBody {
	return self.body
}

// WriteHeader 记下状态码，但**不立刻发出去**。
//
// 状态行一旦发出，头就冻结了：此后 Header().Set 无效，正文也只能追加。旧实现在这里
// 直接透传，于是 `ctx.WriteHeader(422); ctx.RespondByJson(x)` 得到一个光秃秃的 422、
// body 为空 —— Apply 靠 Written() 判"已经回过"，把 Result 静默丢掉。编译通过、vet
// 干净、单测照过，只有真栈 curl 才看得出来；vectors 两个模块同一天踩到，此后写非
// 200 的 JSON 只能绕开 RespondByJson 手写全套。
//
// 现在状态码只是记下来，真正发出去的时机是第一次 Write / Flush，或请求结束时的
// Commit。这与 net/http 自己的约定一致（头在第一次 Write 时冻结），只是把"第一次"
// 往后挪到了真有正文的那一刻，让 WriteHeader 与 Respond* 的先后顺序不再要紧。
//
// 1xx（100 Continue / 103 Early Hints）例外：那是可以发多次的中间响应，必须立刻透传。
func (self *THttpResponse) WriteHeader(s int) {
	if s >= 100 && s < 200 {
		self.ResponseWriter.WriteHeader(s)
		return
	}
	if self.committed {
		// 已经发出去了，再改无效。net/http 对透传的重复 WriteHeader 会记一条
		// superfluous 日志，这里不再重复那条。
		return
	}
	self.status = s
}

// Commit 把记下的状态码发到连接上；幂等。没记过状态码时不动 —— 那由第一次 Write
// 或 net/http 的隐式 200 决定。router 在每条请求结束时调用它，保证"只调了
// WriteHeader(404) 没写正文"的响应也能把 404 发出去。
func (self *THttpResponse) Commit() {
	if self.committed || self.status == 0 || self.ResponseWriter == nil {
		return
	}
	self.committed = true
	self.ResponseWriter.WriteHeader(self.status)
}

// Committed 报告状态行是否已经发出（发出后头就冻结了）。
func (self *THttpResponse) Committed() bool {
	return self.committed
}

// commitBeforeBody 在写正文 / Flush 之前把头发出去；没定过状态码就按 200。
func (self *THttpResponse) commitBeforeBody() {
	if self.status == 0 {
		self.status = http.StatusOK
	}
	self.Commit()
}

// bodyAllowedForStatus 与 net/http 同名函数一字不差：204 / 304 / 1xx 这三类响应
// **不允许有正文**（RFC 9110 §15.3.5、§15.4.5）。
func bodyAllowedForStatus(status int) bool {
	switch {
	case status >= 100 && status <= 199:
		return false
	case status == http.StatusNoContent:
		return false
	case status == http.StatusNotModified:
		return false
	}
	return true
}

// Write 写正文；状态码不允许带正文时按 net/http 的约定回 ErrBodyNotAllowed 并丢弃。
//
// ★ 这一层不是多余的。延迟提交把 WriteHeader 变成"只记状态码"之后，
// `ctx.NotModified(); ctx.Respond(x)` 里的 x 会一路走到 Apply 的 Write —— 而
// 之前 WriteHeader 是立即提交的，Apply 见 Written() 就跳过，顺带把这条挡住了。
//
// 真栈上 net/http 自己会拦（实测：304 + 18 字节正文，客户端收到的仍是 0 字节），
// 所以这不是线上缺陷；漏的是**别的 ResponseWriter**：httptest.ResponseRecorder 照单
// 全收，于是"304 带正文"这件事在单测里看得见、在真栈上看不见，两边判据相反。谁先信了
// recorder，谁就会去改一个并不存在的问题；谁先信了真栈，谁就会漏掉换一个 writer
// （压缩包装、Hijack 后自己接管的连接）之后真的漏出去的那一天。
func (self *THttpResponse) Write(b []byte) (int, error) {
	self.commitBeforeBody()
	if !bodyAllowedForStatus(self.status) {
		return 0, http.ErrBodyNotAllowed
	}
	size, err := self.ResponseWriter.Write(b)
	self.size += size
	return size, err
}

// write data as stream
func (self *THttpResponse) WriteStream(data interface{}) error {
	_, err := self.body.Encode(data)
	if err != nil {
		return err
	}

	_, err = self.Write(self.body.Data.Bytes())
	if err != nil {
		return err
	}

	return nil
}

// Written 报告是否已经有人决定过这条响应（记过状态码或写过正文）。
// 注意它不等于"头已发出"，那是 Committed。
func (self *THttpResponse) Written() bool {
	return self.status != 0
}

// Flush 把已缓冲的正文推到连接上。http.Flusher 的语义是"头也一并发出"，所以先 Commit。
func (self *THttpResponse) Flush() {
	flusher, ok := self.ResponseWriter.(http.Flusher)
	if ok {
		self.commitBeforeBody()
		flusher.Flush()
	}
}

// Hijack让调用者接管连接,在调用Hijack()后,http server库将不再对该连接进行处理,对于该连接的管理和关闭责任将由调用者接管.
func (self *THttpResponse) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := self.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("the ResponseWriter doesn't support the Hijacker interface")
	}
	return hijacker.Hijack()
}

func (self *THttpResponse) Status() int {
	return self.status
}

func (self *THttpResponse) Size() int {
	return self.size
}

func (self *THttpResponse) Close() {
	rwc, buf, _ := self.ResponseWriter.(http.Hijacker).Hijack()
	if buf != nil {
		buf.Flush()
	}

	if rwc != nil {
		rwc.Close()
	}
	//	Trace("THttpResponse.Close")
}

// Inite and Connect a new ResponseWriter when a new request is coming
func (self *THttpResponse) Connect(w http.ResponseWriter) {
	self.ResponseWriter = w
	if self.body.Data.Len() > 0 {
		self.body.Data.Reset()
	}
	self.status = 0
	self.committed = false
	self.size = 0
}

func (self *THttpResponse) Value() reflect.Value {
	return self.Val
}
