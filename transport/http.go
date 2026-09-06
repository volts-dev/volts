package transport

import (
	"bufio"
	"crypto/tls"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

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

	network := dialCfg.Network
	if network == "" {
		network = "tcp"
	}

	// 代理：WithDialer 给的优先，其次按 WithProxyURL 现建一个。
	//
	// 这两个此前几乎没有读取方 —— ProxyURL **一个都没有**，dialer 只在下面的 JA3
	// 分支里被用到。于是「配了代理但没配指纹」的请求（client.HttpClient 的默认形态）
	// 直接从本机 IP 发出去，而调用方以为自己走了代理。配代理恰恰是为了不让对端看到
	// 这个 IP，静默绕过等于反着做，且不留任何痕迹。
	dialer := dialCfg.dialer
	if dialer == nil && dialCfg.ProxyURL != "" {
		d, err := NewProxyDialer(dialCfg.ProxyURL, "")
		if err != nil {
			return nil, err
		}
		dialer = d
	}

	// 没有代理时保留原来的 newConn 包装：它认环境变量里的代理。
	rawDial := func() (net.Conn, error) {
		if dialer != nil {
			return dialer.Dial(network, addr)
		}
		return newConn(func(a string) (net.Conn, error) {
			return net.DialTimeout(network, a, dialCfg.DialTimeout)
		})(addr)
	}

	var conn net.Conn

	if dialCfg.Secure || self.config.TlsConfig != nil {
		config := self.config.TlsConfig
		if config == nil {
			// 默认开启证书校验。跳过校验须显式：TLSConfig(&tls.Config{InsecureSkipVerify: true})
			config = &tls.Config{}
		}
		// 强制 MinVersion >= TLS 1.2 —— TLS 1.0/1.1 已被 RFC 8996 弃用，存在降级风险。
		// 用户若显式设置了更高的 MinVersion 不覆盖；只兜底未设置或更低的情况。
		if config.MinVersion < tls.VersionTLS12 {
			config = config.Clone()
			config.MinVersion = tls.VersionTLS12
		}

		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			host = addr
		}

		rawConn, err := rawDial()
		if err != nil {
			return nil, err
		}

		if dialCfg.Ja3.Ja3 != "" {
			// JA3 指纹伪装的实现在独立子模块 core/ja3 里（见 ja3_hook.go）。
			// 未注册时**明确报错**而不是静默退回普通 TLS —— 后者会让伪装悄悄失效，
			// 调用方还以为自己伪装了。
			dial := ja3Dialer()
			if dial == nil {
				rawConn.Close()
				return nil, ErrJa3NotRegistered
			}

			conn, err = dial(rawConn, host, config, dialCfg.Ja3.Ja3)
			if err != nil {
				rawConn.Close()
				return nil, err
			}
		} else {
			// tls.Client 不会像 tls.Dial 那样从地址推出 ServerName，不补的话证书
			// 校验必然失败，而报出来的是"证书名字对不上"，不指向这里。
			if config.ServerName == "" {
				config = config.Clone()
				config.ServerName = host
			}
			// tls.DialWithDialer 会把拨号超时同时用作握手超时；这里手工补上，
			// 否则一个只完成 TCP、不回 ServerHello 的对端能把调用方挂住。
			if dialCfg.DialTimeout > 0 {
				rawConn.SetDeadline(time.Now().Add(dialCfg.DialTimeout))
			}
			tlsConn := tls.Client(rawConn, config)
			if err = tlsConn.Handshake(); err != nil {
				rawConn.Close()
				return nil, err
			}
			if dialCfg.DialTimeout > 0 {
				rawConn.SetDeadline(time.Time{})
			}
			conn = tlsConn
		}
	} else {
		c, err := rawDial()
		if err != nil {
			return nil, err
		}
		conn = c
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

	if addr == "" {
		addr = self.config.Addrs
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
						hosts = make([]string, 0)
						for _, ip := range vaddr.IPs() {
							hosts = append(hosts, ip.String())
						}
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
