package main

import (
	"context"

	"github.com/volts-dev/logger"
	"github.com/volts-dev/volts"
	"github.com/volts-dev/volts/client"
	"github.com/volts-dev/volts/router"
	"github.com/volts-dev/volts/server"
	"github.com/volts-dev/volts/test"
	"github.com/volts-dev/volts/transport"
)

type (
	arith struct {
	}

	Arith interface {
		Mul(hd *router.TRpcContext, args *test.Args, reply *test.Reply) error
	}
)

func (t arith) Mul(hd *router.TRpcContext, args *test.Args, reply *test.Reply) error {
	hd.Info("IP:")
	reply.Flt = 0.01001
	reply.Str = "Mul"
	reply.Num = args.Num1 * args.Num2

	hd.Info("Mul2", t, args, *reply)
	return nil
}

func main() {
	r := router.New()
	r.Url("CONNECT", "Arith", new(arith))

	srv := server.New(
		server.Router(r),
	)

	app := volts.New(
		volts.Server(srv),
		volts.Transport(transport.NewTCPTransport()),
		volts.Debug(),
	)

	go app.Run()

	service := "Arith.Mul"
	endpoint := "Test.Endpoint"
	address := "127.0.0.1:35999"

	req, _ := client.NewHttpRequest(service, endpoint, nil)
	cli := client.NewHttpClient()
	// test calling remote address
	if _, err := cli.Call(context.Background(), req, client.WithAddress(address)); err != nil {
		logger.Err("call with address error:", err)
	}

	<-make(chan byte)
}
