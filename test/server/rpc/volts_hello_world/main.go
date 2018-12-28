package main

import (
	"vectors/volts/server"
	"vectors/volts/test"
)

type (
	Arith struct {
	}
)

func (t Arith) Mul(hd *server.TRpcHandler, args *test.Args, reply *test.Reply) error {
	hd.Info("IP:")
	reply.Flt = 0.01001
	reply.Str = "Mul"
	reply.Num = args.Num1 * args.Num2

	hd.Info("Mul2", t, args, *reply)
	return nil
}

func main() {
	srv := server.NewServer()
	srv.Url("CONNECT", "Arith", new(Arith))
	srv.Listen("RPC")
}
