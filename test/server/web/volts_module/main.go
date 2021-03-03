package main

import (
	"github.com/volts-dev/volts/server"
	"github.com/volts-dev/volts/test/server/web/volts_module/module/base"
	"github.com/volts-dev/volts/test/server/web/volts_module/module/web"
)

func main() {
	srv := server.NewServer()
	//srv.RegisterMiddleware(event.NewEvent())
	//srv.Url("GET", "/", ctrls.index)
	srv.RegisterModule(base.Base)
	srv.RegisterModule(web.Web)

	srv.SetTemplateVar("VOLTS", "Hi Guy")
	// serve as a http server
	srv.Listen("http")
}
