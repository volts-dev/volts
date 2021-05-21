package rpc

import (
	"context"

	"github.com/volts-dev/volts/protocol"
)

type (
	Request struct {
		Header     protocol.Header
		Message    *protocol.TMessage
		RemoteAddr string

		// ctx is either the client or server context. It should only
		// be modified via copying the whole Request using WithContext.
		// It is unexported to prevent people from using Context wrong
		// and mutating the contexts held by callers of the same request.
		ctx context.Context
	}
)

func NewRequest(msg *protocol.TMessage, ctx context.Context) *Request {
	return &Request{
		Header:  *msg.Header,
		Message: msg,
		ctx:     ctx,
	}
}
