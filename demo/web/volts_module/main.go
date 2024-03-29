package main

import (
	"github.com/volts-dev/volts"
	"github.com/volts-dev/volts/demo/web/volts_module/group/base"
	"github.com/volts-dev/volts/demo/web/volts_module/group/web"
	"github.com/volts-dev/volts/router"
	"github.com/volts-dev/volts/server"
)

func main() {
	r := router.New()
	r.RegisterGroup(base.Base)
	r.RegisterGroup(web.Web)

	r.SetTemplateVar("VOLTS", "Hi Guy")
	srv := server.New(
		server.WithRouter(r),
	)

	// serve as a http server
	app := volts.New(
		volts.Server(srv),
		volts.Debug(),
	)
	app.Run()
}
