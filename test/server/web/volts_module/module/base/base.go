package base

import (
	"github.com/volts-dev/volts/server"
)

var Base *server.TModule

func init() {
	Base = server.NewModule()
	Base.Url("GET", "/", ctrl.index)
}
