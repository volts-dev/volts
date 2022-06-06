package transport

import (
	"context"

	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/util/body"
)

type RpcRequest struct {
	Message    *Message
	RemoteAddr string

	// Context is either the client or server context. It should only
	// be modified via copying the whole Request using WithContext.
	// It is unexported to prevent people from using Context wrong
	// and mutating the contexts held by callers of the same request.
	Context context.Context

	service     string
	method      string
	endpoint    string
	contentType string
	socket      ISocket //
	Codec       codec.ICodec
	header      map[string]string
	body        *body.TBody //
	rawBody     interface{}
	stream      bool
	first       bool
}

func NewRpcRequest(ctx context.Context, msg *Message, socket ISocket) *RpcRequest {
	// new a body
	body := &body.TBody{
		Codec: codec.IdentifyCodec(msg.SerializeType()),
	}
	body.Data.Write(msg.Payload)

	return &RpcRequest{
		Message: msg,
		Context: ctx,
		socket:  socket,
		body:    body,
		Codec:   codec.IdentifyCodec(msg.SerializeType()), // TODO 判断合法
	}
}

func (r *RpcRequest) Body() *body.TBody {
	return r.body
}

func (r *RpcRequest) ContentType() string {
	return r.contentType
}

func (r *RpcRequest) Service() string {
	return r.service
}

func (r *RpcRequest) Method() string {
	return r.method
}

func (r *RpcRequest) Endpoint() string {
	return r.endpoint
}

func (r *RpcRequest) Header() map[string]string {
	return r.header
}

func (r *RpcRequest) ___Read() ([]byte, error) {
	// got a body
	if r.first {
		b := r.Body()
		r.first = false
		return b.AsBytes(), nil
	}

	var msg Message
	err := r.socket.Recv(&msg)
	if err != nil {
		return nil, err
	}
	r.header = msg.Header

	return msg.Body, nil
}

func (r *RpcRequest) Stream() bool {
	return r.stream
}
