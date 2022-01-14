package main

import (
	"github.com/volts-dev/volts"
	"github.com/volts-dev/volts/router"
	"github.com/volts-dev/volts/server"
)

type (
	ctrls struct {
		//这里写中间件
	}
)

func (action ctrls) Get(hd *router.THttpContext) {
	hd.Info("Middleware")
	hd.Respond([]byte("Middleware"))
}

func (action ctrls) Post(hd *router.THttpContext) {
	hd.Info("Before")
	hd.Respond([]byte("Before"))
}

func (action ctrls) delete(hd *router.THttpContext) {
	hd.Info("After")
	hd.Respond([]byte("After"))
}

func main() {
	r := router.NewRouter()
	//r.RegisterMiddleware(event.NewEvent())
	r.Url("REST", "/ctrls", new(ctrls))

	srv := server.NewServer()

	// serve as a http server
	app := volts.NewService(
		volts.Server(srv),
		volts.Debug(),
	)
	app.Run()
}
