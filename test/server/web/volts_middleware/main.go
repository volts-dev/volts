package main

import (
	"github.com/volts-dev/middleware/event"
	"github.com/volts-dev/volts/server"
)

type (
	ctrls struct {
		event.TEvent
		//这里写中间件
	}
)

func (action ctrls) index(hd *server.TWebHandler) {
	hd.Info("Middleware")
	hd.RespondString("Middleware")
}

func (action ctrls) Init(router *server.TRouter) {
	router.Server().Logger().Info("Init")
}

func (action ctrls) Before(hd *server.TWebHandler) {
	hd.Info("Before")
	hd.RespondString("Before")
}

func (action ctrls) After(hd *server.TWebHandler) {
	hd.Info("After")
	hd.RespondString("After")
}

func (action ctrls) Panic(hd *server.TWebHandler) {
	hd.Info("Panic")
	hd.RespondString("Panic")
}

func main() {
	srv := server.NewServer()
	srv.RegisterMiddleware(event.NewEvent())
	srv.Url("GET", "/", ctrls.index)

	// serve as a http server
	srv.Listen("http")
}
