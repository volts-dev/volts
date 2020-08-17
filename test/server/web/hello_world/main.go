package main

import (
	"fmt"

	"github.com/volts-dev/volts/server"
)

type (
	ctrls struct {
		//这里写中间件
	}
)

func (self ctrls) hello_world(hd *server.TWebHandler) {
	hd.Infof("hello %v", 11)
	hd.Respond([]byte("Hello World (Controler)!"))
}

func (self ctrls) macth_all(hd *server.TWebHandler) {
	p := hd.PathParams()
	c := fmt.Sprintf(`Hello World (Controler/Router Matching "%v":"%v" %v)!`, hd.Route.Url.Path, p.FieldByName("all").AsString(), p.FieldByName("all2").AsString())
	hd.Respond([]byte(c))
}

func main() {
	srv := server.NewServer()
	srv.Url("GET", "/", func(hd *server.TWebHandler) {
		hd.Respond([]byte("Hello World"))
	})
	srv.Url("GET", "/1", ctrls.hello_world)
	srv.Url("GET", "/(:all)", ctrls.macth_all)
	srv.Url("GET", "/(:all)/(:all2)/1", ctrls.macth_all)

	// serve as a http server
	srv.Listen("http")
}
