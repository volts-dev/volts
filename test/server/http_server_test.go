package server

import (
	"testing"

	"github.com/volts-dev/volts"
	"github.com/volts-dev/volts/router"
	"github.com/volts-dev/volts/server"
)

func TestProxy(t *testing.T) {
	r := router.New()
	r.Url("GET", "/", router.HttpReverseProxy)

	srv := server.New(
		server.Router(r),
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
		server.Router(r),
	)

	app := volts.New(
		volts.Server(srv),
		//volts.Transport(transport.NewTCPTransport()),
		volts.Debug(),
	)

	app.Run()

}
