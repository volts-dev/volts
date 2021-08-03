package transport

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"reflect"
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
		http.ResponseWriter
		status int
		size   int

		Val reflect.Value
	}
)

func NewResponser() *THttpResponse {
	resp := &THttpResponse{}
	resp.Val = reflect.ValueOf(resp)
	return resp
}

func (self *THttpResponse) WriteHeader(s int) {
	self.status = s
	self.ResponseWriter.WriteHeader(s)
}

func (self *THttpResponse) Write(b []byte) (int, error) {
	if !self.Written() {
		// The status will be StatusOK if WriteHeader has not been called yet
		self.WriteHeader(http.StatusOK)
	}
	size, err := self.ResponseWriter.Write(b)
	self.size += size
	return size, err
}

func (self *THttpResponse) Written() bool {
	return self.status != 0
}

func (self *THttpResponse) Flush() {
	flusher, ok := self.ResponseWriter.(http.Flusher)
	if ok {
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
	self.status = 0
	self.size = 0
}

func (self *THttpResponse) Value() reflect.Value {
	return self.Val
}
