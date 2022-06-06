package test

import (
	"github.com/volts-dev/volts/router"
)

type (
	ArithCtrl struct {
	}

//	Arith interface {
//		Mul(hd *router.TRpcContext, args *test.Args, reply *test.Reply) error
//	}
)

func (t ArithCtrl) Mul(hd *router.TRpcContext) {
	hd.Info("IP:")
	//hd.Request().
	arg := Args{}
	reply := &Reply{}
	err := hd.Request().Body().Decode(&arg)
	if err != nil {
		reply.Str = err.Error()
		hd.Response().Write(reply)
		return
	}

	reply.Num = arg.Num1 * arg.Num2
	reply.Flt = 0.01001 * float64(arg.Num2)
	reply.Str = "Mul"
	//reply.Num = args.Num1 * args.Num2

	//hd.Info("Mul2", t, args, *reply)
	err = hd.Response().Write(reply)
	if err != nil {

	}
	return
}
