package transport

import (
	"crypto/tls"
	"net"

	vaddr "github.com/volts-dev/volts/util/addr"
	vnet "github.com/volts-dev/volts/util/net"
	mls "github.com/volts-dev/volts/util/tls"
)

type tcpTransport struct {
	config *Config
}

func NewTCPTransport(opts ...Option) ITransport {
	cfg := newConfig()

	for _, opt := range opts {
		opt(cfg)
	}

	return &tcpTransport{config: cfg}
}

func (t *tcpTransport) Dial(addr string, opts ...DialOption) (IClient, error) {
	cfg := DialConfig{
		//Timeout: t.config.ConnectTimeout,
	}

	for _, opt := range opts {
		opt(&cfg)
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
		conn, err = tls.DialWithDialer(&net.Dialer{Timeout: t.config.ConnectTimeout}, "tcp", addr, config)
	} else {
		conn, err = net.DialTimeout("tcp", addr, t.config.ConnectTimeout)
	}

	if err != nil {
		return nil, err
	}

	//encBuf := bufio.NewWriter(conn)
	return &tcpTransportClient{
		tcpTransportSocket: *NewTcpTransportSocket(conn, t.config.ReadTimeout, t.config.WriteTimeout),
		transport:          t,
		config:             cfg,
		conn:               conn,
		//encBuf:             encBuf,
		//timeout: t.config.Timeout,
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
						hosts = vaddr.IPs()
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

		l, err = vnet.Listen(addr, fn)
	} else {
		fn := func(addr string) (net.Listener, error) {
			return net.Listen("tcp", addr)
		}

		l, err = vnet.Listen(addr, fn)
	}

	if err != nil {
		return nil, err
	}

	t.config.Listener = &tcpTransportListener{
		transport: t,
		listener:  l,
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
	return "TCP Transport"
}
