package transport

import (
	"bufio"
	"crypto/tls"
	"encoding/gob"
	"net"

	maddr "github.com/asim/go-micro/v3/util/addr"
	mnet "github.com/asim/go-micro/v3/util/net"
	mls "github.com/asim/go-micro/v3/util/tls"
)

type Reader interface {
	ReadHeader(*Message, MessageType) error
	ReadBody(interface{}) error
}

type Writer interface {
	Write(*Message, interface{}) error
}

type tcpTransport struct {
	config *Config
}

func NewTCPTransport(opts ...Option) ITransport {
	var cfg Config
	for _, o := range opts {
		o(&cfg)
	}
	return &tcpTransport{config: &cfg}
}

/*
func (h *tcpTransport) Request(msg Message, sock *Socket, cde codec.ICodec) IRequest {
	return nil
}

func (h *tcpTransport) Response(sock *Socket, cde codec.ICodec) IResponse {
	return nil
}
*/
func (t *tcpTransport) Dial(addr string, opts ...DialOption) (IClient, error) {
	dopts := DialConfig{
		Timeout: DefaultDialTimeout,
	}

	for _, opt := range opts {
		opt(&dopts)
	}

	var conn net.Conn
	var err error

	// TODO: support dial option here rather than using internal config
	if t.config.Secure || t.config.TLSConfig != nil {
		config := t.config.TLSConfig
		if config == nil {
			config = &tls.Config{
				InsecureSkipVerify: true,
			}
		}
		conn, err = tls.DialWithDialer(&net.Dialer{Timeout: dopts.Timeout}, "tcp", addr, config)
	} else {
		conn, err = net.DialTimeout("tcp", addr, dopts.Timeout)
	}

	if err != nil {
		return nil, err
	}

	encBuf := bufio.NewWriter(conn)

	return &tcpTransportClient{
		dialOpts: dopts,
		conn:     conn,
		encBuf:   encBuf,
		enc:      gob.NewEncoder(encBuf),
		dec:      gob.NewDecoder(conn),
		timeout:  t.config.Timeout,
	}, nil
}

func (t *tcpTransport) Listen(addr string, opts ...ListenOption) (IListener, error) {
	var options ListenConfig
	for _, o := range opts {
		o(&options)
	}

	var l net.Listener
	var err error

	// TODO: support use of listen options
	if t.config.Secure || t.config.TLSConfig != nil {
		config := t.config.TLSConfig

		fn := func(addr string) (net.Listener, error) {
			if config == nil {
				hosts := []string{addr}

				// check if its a valid host:port
				if host, _, err := net.SplitHostPort(addr); err == nil {
					if len(host) == 0 {
						hosts = maddr.IPs()
					} else {
						hosts = []string{host}
					}
				}

				// generate a certificate
				cert, err := mls.Certificate(hosts...)
				if err != nil {
					return nil, err
				}
				config = &tls.Config{Certificates: []tls.Certificate{cert}}
			}
			return tls.Listen("tcp", addr, config)
		}

		l, err = mnet.Listen(addr, fn)
	} else {
		fn := func(addr string) (net.Listener, error) {
			return net.Listen("tcp", addr)
		}

		l, err = mnet.Listen(addr, fn)
	}

	if err != nil {
		return nil, err
	}

	t.config.Listener = &tcpTransportListener{
		timeout:  t.config.Timeout,
		listener: l,
	}

	return t.config.Listener, nil
}

func (t *tcpTransport) Init(opts ...Option) error {
	for _, o := range opts {
		o(t.config)
	}
	return nil
}

func (t *tcpTransport) Config() *Config {
	return t.config
}

func (t *tcpTransport) String() string {
	return "tcp"
}
