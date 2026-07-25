package client

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/errors"
	"github.com/volts-dev/volts/internal/body"
	"github.com/volts-dev/volts/internal/header"
	"github.com/volts-dev/volts/transport"
)

// 服务端 router.handleError 把错误原文写在这个消息头里。
const rpcErrorHeader = "__rpc_error__"

type rpcResponse struct {
	header      header.Header
	body        *body.TBody // []byte
	socket      transport.Socket
	contentType codec.SerializeType
	// status 是服务端写在协议头里的消息状态位，StatusOK(0) 为成功。
	// 承载它的 respMsg 收完立刻归还消息池，所以这里存值而不是存消息指针——
	// 早先这里是一个 *transport.Message 字段且从未被赋值，StatusCode() 因此
	// 恒为 0，服务端的 NotFound/Forbidden 在调用方看来与「正常返回空」无异。
	status transport.MessageStatusType
	length int
}

func (self *rpcResponse) Body() *body.TBody {
	return self.body
}

func (self *rpcResponse) ContentType() string {
	return self.contentType.String()
}

func (r *rpcResponse) Header() header.Header {
	return r.header
}

// StatusCode 返回的是**协议消息状态位**（transport.StatusXXX，0 为成功），
// 不是 HTTP 状态码——这与 httpResponse.StatusCode() 的取值域不同。
// 需要跨传输统一判断时用 Call 返回的 error（其 Code 已折算成 HTTP 码）。
func (r *rpcResponse) StatusCode() int {
	return int(r.status)
}

// statusError 把「服务端回了一个非 OK 状态位」翻译成调用方能判断的 error。
// 文案优先取服务端写的 __rpc_error__ 头，其次取 body（状态位场景下通常为空）。
func (r *rpcResponse) statusError(method string) error {
	detail := r.header.Get(rpcErrorHeader)
	if detail == "" && r.body != nil {
		detail = strings.TrimSpace(r.body.Data.String())
	}
	if detail == "" {
		detail = "server returned no detail"
	}

	return errors.New("volts.client", httpCodeOfStatus(r.status),
		fmt.Sprintf("%s: rpc status %s: %s", method, r.status, detail))
}

// httpCodeOfStatus 把消息状态位折算成 HTTP 码，只为让 errors.Error 带上一个
// 各层都认识的 Code（网关据此决定回给前端什么）。
func httpCodeOfStatus(s transport.MessageStatusType) int {
	switch s {
	case transport.StatusUnauthorized:
		return http.StatusUnauthorized
	case transport.StatusForbidden:
		return http.StatusForbidden
	case transport.StatusNotFound:
		return http.StatusNotFound
	case transport.StatusAborted:
		return http.StatusConflict
	case transport.StatusNotImplemented:
		return http.StatusNotImplemented
	case transport.StatusServiceUnavailable:
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}
