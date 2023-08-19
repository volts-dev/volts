package client

import (
	"sync"
	"testing"
	"time"

	"github.com/volts-dev/volts"
	"github.com/volts-dev/volts/client"
	"github.com/volts-dev/volts/internal/test"
	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/router"
	"github.com/volts-dev/volts/server"
	"github.com/volts-dev/volts/transport"
)

func TestHelloworld(t *testing.T) {
	r := router.New()
	r.Url("CONNECT", "Arith", new(test.ArithCtrl))

	srv := server.New(
		server.WithRouter(r),
	)

	app := volts.New(
		volts.Server(srv),
		volts.Transport(transport.NewTCPTransport()),
		volts.Debug(),
	)

	var g sync.WaitGroup
	g.Add(1)

	go func() {
		app.Run()
	}()

	go func() {
		for {
			if app.Server().Started() {
				g.Done()
				break
			}
			time.Sleep(500)
		}
	}()

	g.Wait()
	//<-time.After(3 * time.Second)

	cli := client.NewRpcClient(
		client.Debug(),
		client.WithHost(app.Server().Config().Address),
	)
	arith := test.NewArithCli(cli)

	arg := &test.Args{Num1: 1, Num2: 2, Flt: 0.0123}
	result, err := arith.Mul(arg)
	if err != nil {
		t.Fatal(err)
	}

	logger.Info(result)
}
