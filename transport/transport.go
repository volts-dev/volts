package transport

import (
	"net"
	"time"
)

type (
	ITransport interface {
		Init(...Option) error
		Config() *Config
		Dial(addr string, opts ...DialOption) (IClient, error)       // for client
		Listen(addr string, opts ...ListenOption) (IListener, error) // for server
		String() string
	}

	ISocket interface {
		Recv(*Message) error
		Send(*Message) error
		Close() error
		Local() string  // Local IP
		Remote() string // Remote IP
		Conn() net.Conn // 接口提供更灵活扩展
	}

	IClient interface {
		ISocket
	}

	IListener interface {
		Addr() net.Addr
		Close() error
		Accept() (net.Conn, error)
		//Serve(func(ISocket)) error // 阻塞监听
		Serve(Handler) error // 阻塞监听
		//Sock() ISocket
	}

	// the handler interface
	Handler interface {
		String() string
		Handler() interface{}
	}

	IRequest interface {
		// The service to call
		Service() string
		// The action to take
		Method() string
		// The content type
		ContentType() string
		// write a response directly to the client
		Body() IBody // *body.TBody
	}

	// 提供给服务器客户端最基本接口
	IResponse interface {
		// write a response directly to the client
		Write(interface{}) (int, error)
	}

	IBody interface {
		Read(interface{}) error
		Write(interface{}) error
	}
)

var (
	DefaultTimeout = time.Second * 5
)
