package base

import (
	"github.com/volts-dev/volts/server"
)

type (
	ctrl struct {
	}
)

var BaseMod *server.TModule

func init() {
	BaseMod = server.NewModule()
	BaseMod.Url("GET", "/1", ctrl.index)
}

func (self ctrl) index(hd *server.TWebHandler) {
	hd.RenderTemplate("index.html", nil)
}
