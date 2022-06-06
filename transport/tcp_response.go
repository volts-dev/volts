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

func NewRpcResponse(ctx context.Context, req *RpcRequest, socket ISocket) *RpcResponse {
	body := &body.TBody{
		Codec: codec.IdentifyCodec(req.Message.SerializeType()),
	}

	return &RpcResponse{
		sock:    socket,
		Request: req,
		body:    body,
	}
}

func (self *RpcResponse) Body() *body.TBody {
	return self.body
}

// TODO 写状态
func (self *RpcResponse) WriteHeader(code MessageType) {
	self.Request.Message.SetMessageType(code)
}

func (self *RpcResponse) Write(data interface{}) error {
	self.WriteHeader(MT_RESPONSE)

	msg := self.Request.Message
	_, err := self.body.Encode(data)
	if err != nil {
		return err
	}

	msg.Payload = self.Body().AsBytes()

	return self.sock.Send(msg)
}
