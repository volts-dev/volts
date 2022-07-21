package test

import (
	"github.com/volts-dev/volts/router"
	"github.com/volts-dev/volts/router/middleware/event"
)

type (
	ArithCtrl struct {
	}

	CtrlWithMiddleware struct {
		event.TEvent
		Session *TestSession
	}
)

func (ctrl CtrlWithMiddleware) Init(cfg *router.ControllerConfig) {
	cfg.AddFilter("session", "WithoutMiddlware")
}

func (ctrl CtrlWithMiddleware) Before(ctx *router.THttpContext) {
	ctx.Write([]byte("event:Before"))
}

func (ctrl CtrlWithMiddleware) After(ctx *router.THttpContext) {
	ctx.Write([]byte("event:After"))
}

func (ctrl CtrlWithMiddleware) HelloWorld(ctx *router.THttpContext) {
	ctx.Write([]byte("Hello World!"))
}

func (ctrl CtrlWithMiddleware) WithoutMiddlware(ctx *router.THttpContext) {
	ctx.Write([]byte("WithoutMiddlware"))
}

func (t ArithCtrl) Mul(ctx *router.TRpcContext) {
	log.Info("IP:")
	//hd.Request().
	arg := Args{}
	reply := &Reply{}
	err := ctx.Request().Body().Decode(&arg)
	if err != nil {
		reply.Str = err.Error()
		ctx.Response().WriteStream(reply)
		return
	}

	reply.Num = arg.Num1 * arg.Num2
	reply.Flt = 0.01001 * float64(arg.Num2)
	reply.Str = "Mul"
	//reply.Num = args.Num1 * args.Num2

	//hd.Info("Mul2", t, args, *reply)
	err = ctx.Response().WriteStream(reply)
	if err != nil {
		ctx.Abort(err.Error())
	}
	return
}
