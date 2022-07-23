package event

import (
	"github.com/volts-dev/volts/router"
)

type (
	httpEvent interface {
		Before(ctx *router.THttpContext)
		After(ctx *router.THttpContext)
	}

	rpcEvent interface {
		Before(ctx *router.TRpcContext)
		After(ctx *router.TRpcContext)
	}

	TEvent struct {
	}
)

func NewEvent() router.IMiddleware {
	return &TEvent{}
}

func (self *TEvent) Name() string {
	return "event"
}

func (self *TEvent) Handler(ctx router.IContext) {
	ctrl := ctx.Data().FieldByName("controller").AsInterface()
	switch v := ctx.(type) {
	case *router.THttpContext:
		if c, ok := ctrl.(httpEvent); ok {
			c.Before(v)
			ctx.Next()
			c.After(v)
		}

	case *router.TRpcContext:
		if c, ok := ctrl.(rpcEvent); ok {
			c.Before(v)
			ctx.Next()
			c.After(v)
		}
	}
}
