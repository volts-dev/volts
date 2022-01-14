package main

import (
	"fmt"

	"github.com/volts-dev/volts"

	"github.com/volts-dev/volts/registry/etcd"
	"github.com/volts-dev/volts/router"
	"github.com/volts-dev/volts/server"
)

type (
	ctrls struct {
		//这里写中间件
	}
)

func (self ctrls) hello_world(hd *router.THttpContext) {
	hd.Infof("hello %v", 11)
	hd.Respond([]byte("Hello World (Controler)!"))
}

func (self ctrls) macth_all(hd *router.THttpContext) {
	p := hd.PathParams()
	c := fmt.Sprintf(`Hello World (Controler/Router Matching "%v":"%v" %v)!`, hd.Route().Path, p.FieldByName("all").AsString(), p.FieldByName("all2").AsString())
	hd.Respond([]byte(c))
}

func main() {
	r := router.NewRouter()
	r.Url("GET", "/", func(hd *router.THttpContext) {
		hd.Respond([]byte("Hello World"))
	})
	r.Url("GET", "/1", ctrls.hello_world)
	r.Url("GET", "/(:all)", ctrls.macth_all)
	r.Url("GET", "/(:all)/(:all2)/1", ctrls.macth_all)

	r.Init(
		router.PrintRoutesTree(),
		router.PrintRequest(),
	)

	srv := server.NewServer(
		server.Address(":16888"),
		server.Router(r),
	)

	app := volts.NewService(
		volts.Server(srv),
		volts.Registry(etcd.NewRegistry()),
	)
	app.Run()
}
