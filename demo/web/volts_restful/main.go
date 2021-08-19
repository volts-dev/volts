package main

import (
	"github.com/volts-dev/volts"
	"github.com/volts-dev/volts/server"
)

type (
	ctrls struct {
		//这里写中间件
	}
)

func (action ctrls) Get(hd *server.THttpContext) {
	hd.Info("Middleware")
	hd.Respond([]byte("Middleware"))
}

func (action ctrls) Post(hd *server.THttpContext) {
	hd.Info("Before")
	hd.Respond([]byte("Before"))
}

func (action ctrls) delete(hd *server.THttpContext) {
	hd.Info("After")
	hd.Respond([]byte("After"))
}

func main() {
	srv := server.NewServer()
	//srv.RegisterMiddleware(event.NewEvent())
	srv.Url("REST", "/ctrls", new(ctrls))
	// serve as a http server
	app := volts.NewService(
		volts.Server(srv),
		volts.Debug(),
	)
	app.Run()
}
