package broker

import (
	"fmt"
	"sync"
)

type (
	// IBroker 提供微服务之间的消息交换总线
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

// Default 返回默认 broker。
//
// **没有任何后端被注册时返回 nil**，调用方必须判空。后端靠 init() 注册（通常是
// 一行空导入），一个只用 HTTP/RPC 的进程完全可能没引入任何 broker —— 此前这里
// 会拿到 nil 构造器直接调用而 panic，把"没配 pub/sub"变成启动崩溃。
func Default(opts ...Option) IBroker {
	if defaultBroker == nil {
		defaultBroker = Use("http")
	}
	if defaultBroker == nil {
		return nil
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
	if def == nil {
		// 未注册任何后端。返回 nil 而不是调用 nil 构造器 —— 后者是段错误，
		// 且崩在 init 阶段，栈里看不出"你没导入 broker 后端"这个真正原因。
		return nil
	}
	return def(opts...)
}
