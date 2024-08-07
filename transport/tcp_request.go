package transport

import (
	"context"

	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/internal/body"
	"github.com/volts-dev/volts/internal/header"
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
	codec       codec.ICodec
	header      header.Header
	body        *body.TBody //
	rawBody     interface{}
	stream      bool
	first       bool
}

// 提供给Router的context使用
func NewRpcRequest(ctx context.Context, message *Message, socket ISocket) *RpcRequest {
	r := &RpcRequest{
		Message: message,
		Context: ctx,
		socket:  socket,
	}

	// new a body
	body := body.New(r.Codec())
	body.Data.Write(message.Payload)
	r.body = body

	return r
}

func (self *RpcRequest) Codec() codec.ICodec {
	if self.codec == nil {
		self.codec = codec.IdentifyCodec(self.Message.SerializeType())
	}

	return self.codec
}

func (self *RpcRequest) Body() *body.TBody {
	return self.body
}

func (self *RpcRequest) ContentType() string {
	return self.contentType
}

func (self *RpcRequest) Service() string {
	return self.service
}

func (self *RpcRequest) Method() string {
	return self.method
}

func (self *RpcRequest) Endpoint() string {
	return self.endpoint
}

// header 这是通讯协议包中附带的数据，有区别于body内
func (self *RpcRequest) Header() header.Header {
	if self.header == nil {
		self.header = make(header.Header)
		for k, v := range self.Message.Header {
			self.header.Add(k, v)
		}
	}

	return self.header
}

func (self *RpcRequest) ___Read() ([]byte, error) {
	// got a body
	if self.first {
		b := self.Body()
		self.first = false
		return b.AsBytes(), nil
	}

	var msg Message
	err := self.socket.Recv(&msg)
	if err != nil {
		return nil, err
	}
	//self.header = msg.Header

	return msg.Body, nil
}

func (self *RpcRequest) Stream() bool {
	return self.stream
}
