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

// Config 返回订阅时传入的配置。
//
// 此前返回 nil —— 调用方读 sub.Config().Queue 直接是空指针崩溃，
// 而 ISubscriber 接口并没有说它可以为 nil。broker/memory 一直是正常返回的，
// 所以同一份调用代码换个后端就崩。
func (self *httpSubscriber) Config() *broker.SubscribeConfig {
	return self.opts
}

// Topic 返回订阅的主题。此前返回空串。
func (self *httpSubscriber) Topic() string {
	return self.topic
}

// Unsubscribe 取消这个订阅。
//
// 此前是空实现：调用方以为退订了，handler 却继续被调用，且节点一直留在
// registry 里 —— 直到进程退出为止。server.Stop() 逐个 Unsubscribe 的那圈循环
// 因此从来没有真正生效过。
func (self *httpSubscriber) Unsubscribe() error {
	return self.hb.unsubscribe(self)
}
