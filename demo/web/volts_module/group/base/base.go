package base

import (
	"github.com/volts-dev/volts/router"
)

var Base *router.TGroup

func init() {
	Base = router.NewGroup(router.WithGroupPathPrefix("/base"))
	Base.Url([]string{"GET"}, "/", ctrl.index)
}
