package client

import (
	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/internal/body"
	"github.com/volts-dev/volts/internal/header"
	"github.com/volts-dev/volts/transport"
)

type rpcResponse struct {
	message     *transport.Message
	header      header.Header
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

func (r *rpcResponse) Header() header.Header {
	return r.header
}
