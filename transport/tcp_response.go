package transport

import (
	"context"

	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/internal/body"
)

type RpcResponse struct {
	body    *body.TBody //
	sock    ISocket
	Request *RpcRequest // request for this response
	status  MessageStatusType
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
func (self *RpcResponse) WriteHeader(code MessageStatusType) {
	self.status = code
}

func (self *RpcResponse) Write(b []byte) (int, error) {
	if self.Request.Message.IsOneway() {
		return 0, nil // errors.New("This is one way request!")
	}

	msg := newMessage()
	self.Request.Message.CloneTo(msg)
	msg.SetMessageType(MT_RESPONSE)
	if self.status == 0 {
		self.status = StatusOK
	}
	msg.SetMessageStatusType(self.status)
	msg.Payload = b
	return len(b), self.sock.Send(msg)
}

// write data as stream
func (self *RpcResponse) WriteStream(data interface{}) error {
	if self.Request.Message.IsOneway() {
		return nil // errors.New("This is one way request!")
	}

	msg := self.Request.Message
	msg.SetMessageType(MT_RESPONSE)
	if self.status == 0 {
		self.status = StatusOK
	}
	msg.SetMessageStatusType(self.status)
	_, err := self.body.Encode(data)
	if err != nil {
		return err
	}

	msg.Payload = self.Body().Data.Bytes()
	return self.sock.Send(msg)
}
