package main

import (
	"github.com/volts-dev/volts"
	"github.com/volts-dev/volts-middleware/event"
	"github.com/volts-dev/volts/router"
	"github.com/volts-dev/volts/server"
)

type (
	ctrls struct {
		event.TEvent
		//这里写中间件
	}
)

//func (action ctrls) Init(router *server.TRouter) {
//	router.Server().Logger().Info("Init")
//}

func (action ctrls) index(hd *router.THttpContext) {
	hd.Info("Middleware")
	hd.Respond([]byte("Middleware"))
}

func (action ctrls) Before(hd *router.THttpContext) {
	hd.Info("Before")
	hd.Respond([]byte("Before"))
}

func (action ctrls) After(hd *router.THttpContext) {
	hd.Info("After")
	hd.Respond([]byte("After"))
}

func (action ctrls) Panic(hd *router.THttpContext) {
	hd.Info("Panic")
	hd.Respond([]byte("Panic"))
}

func main() {
	r := router.New()
	//r.RegisterMiddleware(event.NewEvent())
	r.Url("GET", "/", ctrls.index)

	srv := server.New(
		server.WithRouter(r),
	)

	// serve as a http server
	app := volts.New(volts.Server(srv))
	app.Run()
}
