package web

import (
	"github.com/volts-dev/volts/server"
)

var Web *server.TGroup

func init() {
	Web = server.NewGroup()
	Web.Url("GET", "/web", func(hd *server.HttpHandler) {
		hd.Respond([]byte("Hello World"))
	})
}
