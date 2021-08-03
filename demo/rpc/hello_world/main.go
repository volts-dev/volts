package main

import (
	"context"
	"time"

	"github.com/volts-dev/volts"
	"github.com/volts-dev/volts/client"
	test "github.com/volts-dev/volts/demo"
	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/server"
	"github.com/volts-dev/volts/transport"
)

type (
	Arith struct {
	}
)

func (t Arith) Mul(hd *server.RpcHandler, args *test.Args, reply *test.Reply) error {
	hd.Info("IP:")
	reply.Flt = 0.01001
	reply.Str = "Mul"
	reply.Num = args.Num1 * args.Num2

	hd.Info("Mul2", t, args, *reply)
	return nil
}

func main() {
	service := "Arith.Mul"
	endpoint := "Test.Endpoint"
	address := "127.0.0.1:35999"

	srv := server.NewServer()
	srv.Url("CONNECT", "Arith", new(Arith))

	app := volts.NewService(
		volts.Server(srv),
		volts.Transport(transport.NewTCPTransport()),
		volts.Debug(),
	)

	go app.Run()
	time.Sleep(800 * time.Millisecond)

	req := client.NewRequest(service, endpoint, nil)

	// test calling remote address
	if err := client.Call(context.Background(), req, nil, client.WithAddress(address)); err != nil {
		logger.Err("call with address error:", err)
	}

	<-make(chan error, 1)
}
