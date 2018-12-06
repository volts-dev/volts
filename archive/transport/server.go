package server

type (
	IServer interface {
		Options() Options
		Init(...Option) error
		Handle(Handler) error
		NewHandler(interface{}, ...HandlerOption) Handler
		NewSubscriber(string, interface{}, ...SubscriberOption) Subscriber
		Subscribe(Subscriber) error
		Register() error
		Deregister() error
		Start() error
		Stop() error
		String() string
	}
)
