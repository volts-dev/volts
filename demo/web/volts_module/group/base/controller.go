package base

import "github.com/volts-dev/volts/router"

type (
	ctrl struct {
	}
)

func (self ctrl) index(hd *router.THttpContext) {
	hd.RenderTemplate("index.html", nil)
}
