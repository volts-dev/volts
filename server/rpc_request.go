package server

import (
	"context"

	"github.com/volts-dev/volts/transport"
)

type (
	RpcRequest struct {
		Bom        transport.Bom
		Message    *transport.Message
		RemoteAddr string

		// ctx is either the client or server context. It should only
		// be modified via copying the whole Request using WithContext.
		// It is unexported to prevent people from using Context wrong
		// and mutating the contexts held by callers of the same request.
		ctx context.Context
	}
)

func newRpcRequest(msg *transport.Message, ctx context.Context) *RpcRequest {
	return &RpcRequest{
		Bom:     *msg.Bom,
		Message: msg,
		ctx:     ctx,
	}
}
