package bus

type (
	// IBus 提供微服务之前的信息交换总线
	IBus interface {
		String() string // 返回对象名称
		Init(...Option) error
		Config() *Config
		Address() string
		Listen() error // 开始阻塞监听
		Close() error
		Publish(topic string, m *TMessage, opts ...Option) error
		Subscribe(topic string, h Handler, opts ...SubscribeOption) (ISubscriber, error)
	}

	// Subscriber is a convenience return type for the Subscribe method
	ISubscriber interface {
		Config() *SubscribeConfig
		Topic() string
		Unsubscribe() error
	}

	// Event is given to a subscription handler for processing
	Event interface {
		Topic() string
		Message() *TMessage
		Ack() error
		Error() error
	}

	// Handler is used to process messages via a subscription of a topic.
	// The handler is passed a publication interface which contains the
	// message and optional Ack method to acknowledge receipt of the message.
	Handler func(Event) error

	TMessage struct {
		Header map[string]string
		Body   []byte
	}
)
