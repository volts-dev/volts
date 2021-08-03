package base

import (
	"github.com/volts-dev/volts/server"
)

var Base *server.TGroup

func init() {
	Base = server.NewGroup(server.GroupPrefixPath("/base"))
	Base.Url("GET", "/", ctrl.index)
}
