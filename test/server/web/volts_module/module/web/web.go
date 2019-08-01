package web

import (
	"github.com/volts-dev/volts/server"
)

var Web *server.TModule

func init() {
	Web = server.NewModule("/")
	Web.Url("GET", "/web", func(hd *server.TWebHandler) {
		hd.Respond([]byte("Hello World"))
	})
}
