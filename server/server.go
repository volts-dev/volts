package server

type (

	// Message is an async message interface
	xxMessage interface {
		// Topic of the message
		Topic() string
		// The decoded payload value
		Payload() interface{}
		// The content type of the payload
		ContentType() string
		// The raw headers of the message
		Header() map[string]string
		// The raw body of the message
		Body() []byte
		// Codec used to decode the message
		//	Codec() codec.Reader
	}
	// Server is a simple micro server abstraction
	IServer interface {
		// Initialise options
		Init(...Option) error
		// Retrieve the options
		Config() *Config

		Name() string
		// Register a handler
		//Handle(Handler) error
		// Create a new handler
		//NewHandler(interface{}, ...HandlerOption) Handler
		// Create a new subscriber
		//NewSubscriber(string, interface{}, ...SubscriberOption) Subscriber
		// Register a subscriber
		//Subscribe(Subscriber) error
		// Start the server
		Start() error
		// Stop the server
		Stop() error
		// Server implementation
		String() string
	}

	// Router handle serving messages
	IRouter interface {
		String() string
		// 连接入口 serveHTTP 等接口实现
		Handler() interface{}
	}
)
