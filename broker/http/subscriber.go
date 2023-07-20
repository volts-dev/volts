package http

import (
	"github.com/volts-dev/volts/broker"
	"github.com/volts-dev/volts/registry"
)

type (
	httpSubscriber struct {
		opts  *broker.SubscribeConfig
		id    string
		topic string
		fn    broker.Handler
		svc   *registry.Service
		hb    *httpBroker
	}
)

func (self *httpSubscriber) Config() *broker.SubscribeConfig {
	return nil
}

func (self *httpSubscriber) Topic() string {
	return ""
}

func (self *httpSubscriber) Unsubscribe() error {
	return nil
}
