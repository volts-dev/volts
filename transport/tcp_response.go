package transport

import (
	"context"

	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/util/body"
)

type RpcResponse struct {
	body    *body.TBody //
	sock    ISocket
	Request *RpcRequest // request for this response
}

// 提供给Router的context使用
func NewRpcResponse(ctx context.Context, req *RpcRequest, socket ISocket) *RpcResponse {
	return &RpcResponse{
		sock:    socket,
		Request: req,
		body:    body.New(codec.IdentifyCodec(req.Message.SerializeType())),
	}
}

func (self *RpcResponse) Body() *body.TBody {
	return self.body
}

// TODO 写状态
func (self *RpcResponse) WriteHeader(code MessageType) {
	self.Request.Message.SetMessageType(code)
}

func (self *RpcResponse) Write(b []byte) (int, error) {
	if self.Request.Message.IsOneway() {
		return 0, nil // errors.New("This is one way request!")
	}

	self.WriteHeader(MT_RESPONSE)

	msg := newMessage()
	self.Request.Message.CloneTo(msg)
	msg.Payload = b
	return len(b), self.sock.Send(msg)
}

// write data as stream
func (self *RpcResponse) WriteStream(data interface{}) error {
	if self.Request.Message.IsOneway() {
		return nil // errors.New("This is one way request!")
	}

	self.WriteHeader(MT_RESPONSE)

	msg := self.Request.Message
	_, err := self.body.Encode(data)
	if err != nil {
		return err
	}

	msg.Payload = self.Body().Data.Bytes()

	return self.sock.Send(msg)
}
