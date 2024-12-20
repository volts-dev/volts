package transport

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
	vaddr "github.com/volts-dev/volts/internal/addr"
	vnet "github.com/volts-dev/volts/internal/net"
	mls "github.com/volts-dev/volts/internal/tls"
)

// Time wraps time.Time overriddin the json marshal/unmarshal to pass
// timestamp as integer
type Time struct {
	time.Time
}

type data struct {
	Time Time `json:"time"`
}

// A Cookie represents an HTTP cookie as sent in the Set-Cookie header of an
// HTTP response or the Cookie header of an HTTP request.
//
// See https://tools.ietf.org/html/rfc6265 for details.
// Stolen from Net/http/cookies
type Cookie struct {
	Name  string `json:"name"`
	Value string `json:"value"`

	Path        string `json:"path"`   // optional
	Domain      string `json:"domain"` // optional
	Expires     time.Time
	JSONExpires Time   `json:"expires"`    // optional
	RawExpires  string `json:"rawExpires"` // for reading cookies only

	// MaxAge=0 means no 'Max-Age' attribute specified.
	// MaxAge<0 means delete cookie now, equivalently 'Max-Age: 0'
	// MaxAge>0 means Max-Age attribute present and given in seconds
	MaxAge   int           `json:"maxAge"`
	Secure   bool          `json:"secure"`
	HTTPOnly bool          `json:"httpOnly"`
	SameSite http.SameSite `json:"sameSite"`
	Raw      string
	Unparsed []string `json:"unparsed"` // Raw text of unparsed attribute-value pairs
}

// UnmarshalJSON implements json.Unmarshaler inferface.
func (t *Time) UnmarshalJSON(buf []byte) error {
	// Try to parse the timestamp integer
	ts, err := strconv.ParseInt(string(buf), 10, 64)
	if err == nil {
		if len(buf) == 19 {
			t.Time = time.Unix(ts/1e9, ts%1e9)
		} else {
			t.Time = time.Unix(ts, 0)
		}
		return nil
	}
	str := strings.Trim(string(buf), `"`)
	if str == "null" || str == "" {
		return nil
	}
	// Try to manually parse the data
	tt, err := ParseDateString(str)
	if err != nil {
		return err
	}
	t.Time = tt
	return nil
}

// ParseDateString takes a string and passes it through Approxidate
// Parses into a time.Time
func ParseDateString(dt string) (time.Time, error) {
	const layout = "Mon, 02-Jan-2006 15:04:05 MST"

	return time.Parse(layout, dt)
}

type (
	HttpTransport struct {
		sync.Mutex
		config *Config
		//dialer proxy.ContextDialer
		//dialer    proxy.Dialer
		//JA3       string
		//UserAgent string
	}
)

func NewHTTPTransport(opts ...Option) *HttpTransport {
	return &HttpTransport{
		//dialer: proxy.Direct,
		config: newConfig(opts...),
	}
}

func (self *HttpTransport) Init(opts ...Option) error {
	for _, o := range opts {
		o(self.config)
	}
	return nil
}

// to make a Dial with server
func (self *HttpTransport) Dial(addr string, opts ...DialOption) (IClient, error) {
	dialCfg := DialConfig{
		DialTimeout:  self.config.DialTimeout,
		ReadTimeout:  self.config.ReadTimeout,
		WriteTimeout: self.config.WriteTimeout,
	}
	dialCfg.Init(opts...)

	var conn net.Conn
	var err error

	// TODO: support dial option here rather than using internal config
	if dialCfg.Secure || self.config.TlsConfig != nil {
		config := self.config.TlsConfig
		if config == nil {
			config = &tls.Config{
				InsecureSkipVerify: true, // 跳过认证证书
			}
		}

		if dialCfg.Ja3.Ja3 != "" {
			rawConn, err := dialCfg.dialer.Dial(dialCfg.Network, addr)
			if err != nil {
				return nil, err
			}

			var host string
			if host, _, err = net.SplitHostPort(addr); err != nil {
				host = addr
			}
			//////////////////

			spec, err := parseJA3(dialCfg.Ja3.Ja3)
			if err != nil {
				return nil, err
			}

			cnn := utls.UClient(rawConn, &utls.Config{
				ServerName:         host,
				MinVersion:         tls.VersionTLS12,
				MaxVersion:         tls.VersionTLS12,
				InsecureSkipVerify: config.InsecureSkipVerify,
			}, // Default is TLS13
				utls.HelloChrome_Auto)
			if err := cnn.ApplyPreset(spec); err != nil {
				return nil, err
			}

			if err = cnn.Handshake(); err != nil {
				_ = cnn.Close()

				if err.Error() == "tls: CurvePreferences includes unsupported curve" {
					//fix this
					return nil, fmt.Errorf("conn.Handshake() error for tls 1.3 (please retry request): %+v", err)
				}
				return nil, fmt.Errorf("uTlsConn.Handshake() error: %+v", err)
			}

			conn = cnn
		} else {
			//config.NextProtos = []string{"http/1.1"}
			//	conn, err = newConn(func(addr string) (net.Conn, error) {
			//		return tls.DialWithDialer(&net.Dialer{Timeout: self.config.DialTimeout}, "tcp", addr, config)
			//	})(addr)
			conn, err = tls.DialWithDialer(&net.Dialer{Timeout: dialCfg.DialTimeout}, "tcp", addr, config)
		}

	} else {
		conn, err = newConn(func(addr string) (net.Conn, error) {
			return net.DialTimeout("tcp", addr, dialCfg.DialTimeout)
		})(addr)
	}

	if err != nil {
		return nil, err
	}

	return &httpTransportClient{
		transport: self,
		config:    dialCfg,
		addr:      addr,
		conn:      conn,
		buff:      bufio.NewReader(conn),
		r:         make(chan *http.Request, 1),
		local:     conn.LocalAddr().String(),
		remote:    conn.RemoteAddr().String(),
	}, nil
}

func (self *HttpTransport) Listen(addr string, opts ...ListenOption) (IListener, error) {
	var options ListenConfig
	for _, opt := range opts {
		opt(&options)
	}

	var l net.Listener
	var err error
	if self.config.EnableACME && self.config.ACMEProvider != nil {
		// should we check the address to make sure its using :443?
		//l, err = self.config.ACMEProvider.Listen(self.config.ACMEHosts...)
		config, err := self.config.ACMEProvider.TLSConfig(self.config.ACMEHosts...)
		if err != nil {
			return nil, err
		}

		fn := func(addr string) (net.Listener, error) {
			// generate a certificate
			cert, err := mls.Certificate(self.config.ACMEHosts...)
			if err != nil {
				return nil, err
			}
			config.Certificates = []tls.Certificate{cert}
			return tls.Listen("tcp", addr, config)
		}

		l, err = vnet.Listen(addr, fn)
	} else if self.config.Secure || self.config.TlsConfig != nil {
		// TODO: support use of listen options
		config := self.config.TlsConfig

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
		transport: self,
		listener:  l,
	}
	/*
		self.config.Listener = &transportListener{
			transport: self,
			listener:  l,
		}*/
	return self.config.Listener, nil
}

func (self *HttpTransport) Config() *Config {
	return self.config
}

func (self *HttpTransport) String() string {
	if self.config.Secure || self.config.TlsConfig != nil {
		return "HttpsTransport"
	}
	return "HttpTransport"
}

func (self *HttpTransport) Protocol() string {
	if self.config.Secure || self.config.TlsConfig != nil {
		return "https"
	}
	return "http"
}
