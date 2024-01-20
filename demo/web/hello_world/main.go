package main

import (
	"fmt"

	"github.com/volts-dev/volts"
	"github.com/volts-dev/volts/router"
	"github.com/volts-dev/volts/server"
)

type (
	ctrls struct {
		//这里写中间件
	}
)

func (self ctrls) HelloWorld(hd *router.THttpContext) {
	hd.Infof("hello %v", 11)
	hd.Respond([]byte("Hello World (Controler)!"))
}

func (self ctrls) MacthAll(hd *router.THttpContext) {
	p := hd.PathParams()
	c := fmt.Sprintf(`Hello World (Controler/Router Matching "%v":"%v" %v)!`, hd.Route().Path, p.FieldByName("all").AsString(), p.FieldByName("all2").AsString())
	hd.Respond([]byte(c))
}

func main() {
	r := router.New()
	r.Url("GET", "/", func(hd *router.THttpContext) {
		hd.Respond([]byte("Hello World"))
	})
	r.Url("GET", "/1", ctrls.HelloWorld)
	r.Url("GET", "/<:all>", ctrls.MacthAll)
	r.Url("GET", "/<:all>/<:all2>/1", ctrls.MacthAll)

	g1 := router.NewGroup(router.WithGroupPathPrefix("/<:lang>"))
	{
		g1.Url("GET", "/", ctrls.HelloWorld)
		g1.Url("GET", "/<:all>", ctrls.MacthAll)
	}

	r.RegisterGroup(g1)
	r.Config().Init(
		router.WithRoutesTreePrinter(),
		router.WithRequestPrinter(),
		router.WithPprof(),
	)

	srv := server.New(
		server.Address(":16888"),
		server.WithRouter(r),
	)

	app := volts.New(
		volts.Server(srv),
		//volts.Registry(etcd.New()),
	)
	app.Run()
}
