package base

import (
	"github.com/volts-dev/volts/server"
)

type (
	ctrl struct {
	}
)

func (self ctrl) index(hd *server.TWebHandler) {
	hd.RenderTemplate("index.html", nil)
}
