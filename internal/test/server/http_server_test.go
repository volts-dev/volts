package server

import (
	"sync"
	"testing"
	"time"

	"github.com/volts-dev/volts"
	"github.com/volts-dev/volts/client"
	"github.com/volts-dev/volts/internal/test"
	"github.com/volts-dev/volts/router"
	"github.com/volts-dev/volts/router/middleware/event"
	"github.com/volts-dev/volts/server"
)

func TestBase(t *testing.T) {
	r := router.New()
	r.Url("GET", "/", func(ctx *router.THttpContext) {
		ctx.Write([]byte("Hello World!"))
	})
	r.Url("GET", "/1", test.CtrlWithMiddleware.HelloWorld)
	r.Url("GET", "/WithoutMiddlware", test.CtrlWithMiddleware.WithoutMiddlware)

	r.RegisterMiddleware(event.NewEvent)
	r.RegisterMiddleware(test.NewTestSession)

	srv := server.New(
		server.WithRouter(r),
		server.Debug(),
	)

	var g sync.WaitGroup
	g.Add(1)
	go func() {
		if err := srv.Start(); err != nil {
			srv.Stop()
			g.Done()
		}
	}()

	go func() {
		// 等待就绪
		for {
			if srv.Started() {
				g.Done()
				break
			}

			time.Sleep(500)
		}
	}()
	g.Wait()

	cli, err := client.NewHttpClient(
		client.WithHost(srv.Config().Address),
	)
	if err != nil {
		t.Fatal(err)
	}
	req, err := cli.NewRequest("GET", "http://localhost:35999/WithoutMiddlware", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := cli.Call(req)
	if err != nil {
		t.Fatal(err)
	}
	result := resp.Body().Data.String()
	if result != "Hello World!" {
		t.Log(result)
	}
}

func TestProxy(t *testing.T) {
	r := router.New()
	r.Url("GET", "/", router.HttpReverseProxy)

	srv := server.New(
		server.WithRouter(r),
	)

	app := volts.New(
		volts.Server(srv),
		//volts.Transport(transport.NewTCPTransport()),
		volts.Debug(),
	)

	app.Run()

}
func TestRecover(t *testing.T) {
	r := router.New()
	r.Url("GET", "/", func(ctx *router.THttpContext) {
		p := ctx.PathParams()
		p.FieldByName("query").AsString()
	})

	srv := server.New(
		server.WithRouter(r),
	)

	app := volts.New(
		volts.Server(srv),
		//volts.Transport(transport.NewTCPTransport()),
		volts.Debug(),
	)

	app.Run()

}
