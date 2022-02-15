package client

import (
	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/transport"
	"github.com/volts-dev/volts/util/body"
)

type rpcResponse struct {
	message     *transport.Message
	header      map[string]string
	body        *body.TBody // []byte
	socket      transport.Socket
	contentType codec.SerializeType
	length      int
}

func (self *rpcResponse) Body() *body.TBody {
	return self.body
}

func (self *rpcResponse) ContentType() string {
	return self.contentType.String()
}

func (r *rpcResponse) Header() map[string]string {
	return r.header
}
