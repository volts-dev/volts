package client

import (
	"sync"
	"testing"

	"github.com/volts-dev/logger"
	"github.com/volts-dev/volts"
	"github.com/volts-dev/volts/client"
	"github.com/volts-dev/volts/router"
	"github.com/volts-dev/volts/server"
	"github.com/volts-dev/volts/test"
	"github.com/volts-dev/volts/transport"
)

func TestHelloworld(t *testing.T) {
	r := router.NewRouter()
	r.Url("CONNECT", "Arith", new(test.ArithCtrl))

	srv := server.NewServer(
		server.Router(r),
	)

	app := volts.NewService(
		volts.Server(srv),
		volts.Transport(transport.NewTCPTransport()),
		volts.Debug(),
	)

	var g sync.WaitGroup
	g.Add(1)

	go func() {
		g.Done()
		app.Run()
	}()

	g.Wait()
	//<-time.After(3 * time.Second)

	arg := &test.Args{Num1: 1, Num2: 2, Flt: 0.0123}

	arith := test.NewArithCli(client.NewRpcClient())
	result, err := arith.Mul(arg)
	if err != nil {
		t.Fatal(err)
	}

	logger.Info(result)
}
