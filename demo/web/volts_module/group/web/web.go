package web

import (
	"github.com/volts-dev/volts/server"
)

var Web *server.TGroup

func init() {
	Web = server.NewGroup()
	Web.Url("GET", "/web", func(hd *server.THttpContext) {
		hd.Respond([]byte("Hello World"))
	})
}
