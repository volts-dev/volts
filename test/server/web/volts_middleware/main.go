package main

import (
	"vectors/middleware/event"
	"vectors/volts/server"
)

type (
	ctrls struct {
		event.TEvent
		//这里写中间件
	}
)

func (action ctrls) Get(hd *server.TWebHandler) {
	hd.RespondString("Get")
}

func (action ctrls) Before(hd *server.TWebHandler) {
	hd.RespondString("Before")
}

func (action ctrls) After(hd *server.TWebHandler) {
	hd.Logger.Info("After")
}

func (action TAction) Panic(hd *server.TWebHandler) {
	hd.Logger.Info("After")
}
