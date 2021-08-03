package client

import (
	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/transport"
)

type rpcResponse struct {
	header map[string]string
	body   []byte
	socket transport.Socket
	codec  codec.ICodec
}

func (r *rpcResponse) Codec() codec.ICodec {
	return r.codec
}

func (r *rpcResponse) Header() map[string]string {
	return r.header
}

func (r *rpcResponse) Read() ([]byte, error) {
	var msg transport.Message

	if err := r.socket.Recv(&msg); err != nil {
		return nil, err
	}

	// set internals
	r.header = msg.Header
	r.body = msg.Body

	return msg.Body, nil
}
