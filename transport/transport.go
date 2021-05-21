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
		//Request(Message, *Socket, codec.ICodec) IRequest
		//Response(*Socket, codec.ICodec) IResponse
		String() string
	}

	ISocket interface {
		Recv(*Message) error
		Send(*Message) error
		Close() error
		Local() string  // Local IP
		Remote() string // Remote IP
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

		Sock() ISocket
	}

	Handler interface {
		String() string
		Handler() interface{}
	}

	IResponse interface {
		// write a response directly to the client
		Write([]byte) (int, error)
	}
)

var (
	DefaultTransport   ITransport = NewHTTPTransport()
	DefaultDialTimeout            = time.Second * 5
)
