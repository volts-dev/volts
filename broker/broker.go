package broker

import (
	"fmt"
	"sync"
)

type (
	// IBroker 提供微服务之前的信息交换总线
	IBroker interface {
		String() string // 返回对象名称
		Init(...Option) error
		Config() *Config
		Address() string
		Start() error // 开始阻塞监听
		Close() error
		Publish(topic string, m *Message, opts ...PublishOption) error
		Subscribe(topic string, h Handler, opts ...SubscribeOption) (ISubscriber, error)
	}

	// Subscriber is a convenience return type for the Subscribe method
	ISubscriber interface {
		Config() *SubscribeConfig
		Topic() string
		Unsubscribe() error
	}

	// Event is given to a subscription handler for processing
	IEvent interface {
		Topic() string
		Message() *Message
		Ack() error
		Error() error
	}

	// Handler is used to process messages via a subscription of a topic.
	// The handler is passed a publication interface which contains the
	// message and optional Ack method to acknowledge receipt of the message.
	Handler func(IEvent) error
)

var brokerMap = make(map[string]func(opts ...Option) IBroker)
var brokerHost sync.Map
var defaultBroker IBroker

func Default(opts ...Option) IBroker {
	if defaultBroker == nil {
		defaultBroker = Use("http")
	}
	defaultBroker.Init(opts...)
	return defaultBroker
}

func Register(name string, creator func(opts ...Option) IBroker) {
	brokerMap[name] = creator
}

func Use(name string, opts ...Option) IBroker {
	cfg := &Config{Name: name}
	cfg.Init(opts...)

	// 加载存在的服务
	for _, addr := range cfg.Addrs {
		if addr == "" {
			continue
		}

		altName := fmt.Sprintf("%s-%s", cfg.Name, addr)
		if reg, has := brokerHost.Load(altName); has {
			b := reg.(IBroker)
			b.Init(opts...)
			return b
		}
	}

	if fn, has := brokerMap[name]; has {
		reg := fn(opts...)
		for _, addr := range reg.Config().Addrs {
			if addr == "" {
				continue
			}

			regName := fmt.Sprintf("%s-%s", cfg.Name, addr)
			brokerHost.Store(regName, reg)
		}
		return reg
	}

	// 默认
	def := brokerMap["http"]
	return def(opts...)
}
