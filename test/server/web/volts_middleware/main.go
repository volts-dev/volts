package main

import (
	//"github.com/volts-dev/volts-middleware/event"
	"volts-dev/volts-middleware/event"

	"github.com/volts-dev/volts/server"
	//"volts-dev/volts/server"
)

type (
	ctrls struct {
		event.TEvent
		//这里写中间件
	}
)

func (action ctrls) Init(router *server.TRouter) {
	router.Server().Logger().Info("Init")
}

func (action ctrls) index(hd *server.TWebHandler) {
	hd.Info("Middleware")
	hd.Respond([]byte("Middleware"))
}

func (action ctrls) Before(hd *server.TWebHandler) {
	hd.Info("Before")
	hd.Respond([]byte("Before"))
}

func (action ctrls) After(hd *server.TWebHandler) {
	hd.Info("After")
	hd.Respond([]byte("After"))
}

func (action ctrls) Panic(hd *server.TWebHandler) {
	hd.Info("Panic")
	hd.Respond([]byte("Panic"))
}

func main() {
	srv := server.NewServer()
	srv.RegisterMiddleware(event.NewEvent())
	srv.Url("GET", "/", ctrls.index)

	// serve as a http server
	srv.Listen("http")
}
