package transport

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"

	utls "github.com/refraction-networking/utls"
	vaddr "github.com/volts-dev/volts/util/addr"
	vnet "github.com/volts-dev/volts/util/net"
	mls "github.com/volts-dev/volts/util/tls"
	"golang.org/x/net/proxy"
)

type (
	httpTransport struct {
		config *Config
		dialer proxy.ContextDialer
	}
)

func NewHTTPTransport(opts ...Option) *httpTransport {
	cfg := newConfig()

	for _, o := range opts {
		o(cfg)
	}

	return &httpTransport{config: cfg}
}

func (self *httpTransport) Init(opts ...Option) error {
	for _, o := range opts {
		o(self.config)
	}

	return nil
}

func (rt *httpTransport) dialTLS(ctx context.Context, network, addr string) (net.Conn, error) {

	return nil, nil
}

// to make a Dial with server
func (self *httpTransport) Dial(addr string, opts ...DialOption) (IClient, error) {
	cfg := DialConfig{
		//Timeout: DefaultDialTimeout,
	}

	for _, opt := range opts {
		opt(&cfg)
	}

	var conn net.Conn
	var err error

	// TODO: support dial option here rather than using internal config
	if self.config.Secure || self.config.TLSConfig != nil {
		config := self.config.TLSConfig
		if config == nil {
			config = &tls.Config{
				InsecureSkipVerify: true,
			}
		}

		// 代理
		var dialer proxy.ContextDialer
		if cfg.ProxyURL != "" {
			dialer, err = newConnectDialer(cfg.ProxyURL, "")
			if err != nil {

			}
		} else {
			dialer = proxy.Direct
		}

		if cfg.Ja3.Ja3 != "" {

			rawConn, err := dialer.DialContext(context.Background(), "tcp", addr)
			if err != nil {
				return nil, err
			}

			var host string
			if host, _, err = net.SplitHostPort(addr); err != nil {
				host = addr
			}
			//////////////////

			spec, err := stringToSpec(cfg.Ja3.Ja3)
			if err != nil {
				return nil, err
			}

			conn := utls.UClient(rawConn, &utls.Config{
				ServerName: host,
				MinVersion: tls.VersionTLS12,
				MaxVersion: tls.VersionTLS12,
			}, // Default is TLS13
				utls.HelloCustom)
			if err := conn.ApplyPreset(spec); err != nil {
				return nil, err
			}

			if err = conn.Handshake(); err != nil {
				_ = conn.Close()

				if err.Error() == "tls: CurvePreferences includes unsupported curve" {
					//fix this
					return nil, fmt.Errorf("conn.Handshake() error for tls 1.3 (please retry request): %+v", err)
				}
				return nil, fmt.Errorf("uTlsConn.Handshake() error: %+v", err)
			}

		} else {
			config.NextProtos = []string{"http/1.1"}
			conn, err = newConn(func(addr string) (net.Conn, error) {
				return tls.DialWithDialer(&net.Dialer{Timeout: self.config.ConnectTimeout}, "tcp", addr, config)
			})(addr)
		}

	} else {
		conn, err = newConn(func(addr string) (net.Conn, error) {
			return net.DialTimeout("tcp", addr, self.config.ConnectTimeout)
		})(addr)
	}

	if err != nil {
		return nil, err
	}

	return &httpTransportClient{
		ht:     self,
		config: cfg,
		addr:   addr,
		conn:   conn,
		buff:   bufio.NewReader(conn),
		r:      make(chan *http.Request, 1),
		local:  conn.LocalAddr().String(),
		remote: conn.RemoteAddr().String(),
	}, nil
}

func (self *httpTransport) Listen(addr string, opts ...ListenOption) (IListener, error) {
	var options ListenConfig
	for _, opt := range opts {
		opt(&options)
	}

	var l net.Listener
	var err error

	// TODO: support use of listen options
	if self.config.Secure || self.config.TLSConfig != nil {
		config := self.config.TLSConfig

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

	self.config.Listener = &httpTransportListener{
		ht:       self,
		listener: l,
	}

	return self.config.Listener, nil
}

/*
func (h *httpTransport) Request(msg Message, sock *Socket, cde codec.ICodec) IRequest {
	return nil
}

func (h *httpTransport) Response(sock *Socket, cde codec.ICodec) IResponse {
	return nil
}
*/
func (self *httpTransport) Config() *Config {
	return self.config
}

func (self *httpTransport) String() string {
	return "Http Transport"
}
