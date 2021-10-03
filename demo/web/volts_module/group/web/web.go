package web

import "github.com/volts-dev/volts/router"

var Web *router.TGroup

func init() {
	Web = router.NewGroup()
	Web.Url("GET", "/web", func(hd *router.THttpContext) {
		hd.Respond([]byte("Hello World"))
	})
}
