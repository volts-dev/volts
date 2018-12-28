package main

import (
	"vectors/volts/server"
	"vectors/volts/test/server/web/volts_module/module/base"
)

func main() {
	srv := server.NewServer()
	//srv.RegisterMiddleware(event.NewEvent())
	//srv.Url("GET", "/", ctrls.index)
	srv.RegisterModule(base.BaseMod)
	srv.SetTemplateVar("VOLTS", "Hi Guy")
	// serve as a http server
	srv.Listen("http")
}
