package transport

import (
	"context"

	"github.com/volts-dev/volts/codec"
)

type RpcRequest struct {
	//Header     protocol.Header
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
	socket      ISocket
	codec       codec.ICodec
	header      map[string]string
	body        []byte
	rawBody     interface{}
	stream      bool
	first       bool
}

func (r *RpcRequest) Codec() codec.ICodec {
	return r.codec
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

func (r *RpcRequest) Body() interface{} {
	return r.rawBody
}

func (r *RpcRequest) Read() ([]byte, error) {
	// got a body
	if r.first {
		b := r.body
		r.first = false
		return b, nil
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
