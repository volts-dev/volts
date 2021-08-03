package main

import (
	"github.com/volts-dev/volts"
	"github.com/volts-dev/volts/demo/web/volts_module/group/base"
	"github.com/volts-dev/volts/demo/web/volts_module/group/web"
	"github.com/volts-dev/volts/server"
)

func main() {
	srv := server.NewServer()
	//srv.RegisterMiddleware(event.NewEvent())
	//srv.Url("GET", "/", ctrls.index)
	srv.RegisterGroup(base.Base)
	srv.RegisterGroup(web.Web)

	srv.SetTemplateVar("VOLTS", "Hi Guy")
	// serve as a http server
	// serve as a http server
	app := volts.NewService(
		volts.Server(srv),
		volts.Debug(),
	)
	app.Run()
}
