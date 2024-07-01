package transport

import (
	"context"
	"crypto/tls"
	"strings"
	"time"

	"github.com/volts-dev/volts/config"
	"github.com/volts-dev/volts/internal/acme"
	"github.com/volts-dev/volts/internal/acme/autocert"
	"github.com/volts-dev/volts/logger"
	"golang.org/x/net/proxy"
)

var (
	log            = logger.New("Transport")
	DefaultTimeout = 15 * time.Second
)

type (
	Option       func(*Config)
	DialOption   func(*DialConfig)
	ListenOption func(*ListenConfig)

	Config struct {
		config.Config
		Name       string `field:"-"` // config name/path in config file
		PrefixName string `field:"-"` // config prefix name
		Listener   IListener
		// Addrs is the list of intermediary addresses to connect to
		Addrs []string
		// Codec is the codec interface to use where headers are not supported
		// by the transport and the entire payload must be encoded
		//Codec codec.Marshaler

		// 证书
		EnableACME   bool          `field:"enable_acme"`
		ACMEHosts    []string      `field:"acme_hosts"`
		ACMEProvider acme.Provider `field:"-"`
		// Secure tells the transport to secure the connection.
		// In the case TLSConfig is not specified best effort self-signed
		// certs should be used
		Secure bool
		// TLSConfig to secure the connection. The assumption is that this
		// is mTLS keypair
		TlsConfig *tls.Config

		//DialTimeout sets timeout for dialing
		DialTimeout time.Duration
		// ReadTimeout sets readdeadline for underlying net.Conns
		ReadTimeout time.Duration
		// WriteTimeout sets writedeadline for underlying net.Conns
		WriteTimeout time.Duration

		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context
	}

	DialConfig struct {
		// Tells the transport this is a streaming connection with
		// multiple calls to send/recv and that send may not even be called
		Stream bool
		// Other options for implementations of the interface
		// can be stored in a context
		Secure      bool
		DialTimeout time.Duration
		// ReadTimeout sets readdeadline for underlying net.Conns
		ReadTimeout time.Duration
		// WriteTimeout sets writedeadline for underlying net.Conns
		WriteTimeout time.Duration

		// TODO: add tls options when dialling
		// Currently set in global options
		Ja3      Ja3 // TODO 添加加缓存
		ProxyURL string
		dialer   proxy.Dialer
		Network  string

		Context context.Context
	}

	ListenConfig struct {
		// TODO: add tls options when listening
		// Currently set in global options

		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context
	}
)

func (self *DialConfig) Init(opts ...DialOption) {
	for _, opt := range opts {
		if opt != nil {
			opt(self)
		}
	}
}

func newConfig(opts ...Option) *Config {
	cfg := &Config{
		//Name:         "transport",
		DialTimeout:  DefaultTimeout,
		ReadTimeout:  DefaultTimeout,
		WriteTimeout: DefaultTimeout,
	}
	cfg.Init(opts...)
	config.Register(cfg)
	return cfg
}

func (self *Config) String() string {
	if len(self.PrefixName) > 0 {
		return strings.Join([]string{self.PrefixName, "transport"}, ".")
	}
	return self.Name
}

func (self *Config) Init(opts ...Option) {
	for _, opt := range opts {
		if opt != nil {
			opt(self)
		}
	}
}

func (self *Config) Load() error {
	if err := self.LoadToModel(self); err != nil {
		return err
	}

	// 打开了SSL需要指定自动更新服务者
	if self.EnableACME && self.ACMEProvider == nil {
		// 默认是 Let’s Encrypt
		self.ACMEProvider = autocert.NewProvider()
	}

	return nil
}

func (self *Config) Save(immed ...bool) error {
	return self.SaveFromModel(self, immed...)
}

func Debug() Option {
	return func(cfg *Config) {
		cfg.Debug = true
		cfg.ReadTimeout = 60 * time.Second
		cfg.WriteTimeout = 60 * time.Second
		cfg.DialTimeout = 60 * time.Second
	}
}

func Logger() logger.ILogger {
	return log
}

// Addrs to use for transport
func Addrs(addrs ...string) Option {
	return func(cfg *Config) {
		cfg.Addrs = addrs
	}
}

// Timeout sets the timeout for Send/Recv execution
func Timeout(t time.Duration) Option {
	return func(cfg *Config) {
		cfg.ReadTimeout = t
		cfg.WriteTimeout = t
		cfg.DialTimeout = t
	}
}

// Timeout sets the timeout for Send/Recv execution
func ReadTimeout(t time.Duration) Option {
	return func(cfg *Config) {
		cfg.ReadTimeout = t
	}
}

// Timeout sets the timeout for Send/Recv execution
func WriteTimeout(t time.Duration) Option {
	return func(cfg *Config) {
		cfg.WriteTimeout = t
	}
}

// Timeout sets the timeout for Send/Recv execution
func DialTimeout(t time.Duration) Option {
	return func(cfg *Config) {
		cfg.DialTimeout = t
	}
}

func EnableACME(b bool) Option {
	return func(cfg *Config) {
		cfg.EnableACME = b
	}
}
func ACMEHosts(hosts ...string) Option {
	return func(o *Config) {
		o.ACMEHosts = hosts
	}
}

func ACMEProvider(p acme.Provider) Option {
	return func(o *Config) {
		o.ACMEProvider = p
	}
}

// Use secure communication. If TLSConfig is not specified we
// use InsecureSkipVerify and generate a self signed cert
func Secure(b bool) Option {
	return func(cfg *Config) {
		cfg.Secure = b
	}
}

// TLSConfig to be used for the transport.
func TLSConfig(t *tls.Config) Option {
	return func(cfg *Config) {
		cfg.TlsConfig = t
	}
}

func WithNetwork(network string) DialOption {
	return func(o *DialConfig) {
		o.Network = network
	}
}

func WithTLS() DialOption {
	return func(o *DialConfig) {
		o.Secure = true
	}
}

func WithContext(ctx context.Context) DialOption {
	return func(o *DialConfig) {
		o.Context = ctx
	}
}

// Indicates whether this is a streaming connection
func WithStream() DialOption {
	return func(o *DialConfig) {
		o.Stream = true
	}
}

func WithTimeout(dial, read, write time.Duration) DialOption {
	return func(cfg *DialConfig) {
		if dial > 0 {
			cfg.DialTimeout = dial
		}
		if read > 0 {
			cfg.ReadTimeout = read
		}
		if write > 0 {
			cfg.WriteTimeout = write
		}
	}
}

func WithDialTimeout(timeout time.Duration) DialOption {
	return func(cfg *DialConfig) {
		cfg.DialTimeout = timeout
	}
}

func WithReadTimeout(timeout time.Duration) DialOption {
	return func(cfg *DialConfig) {
		cfg.ReadTimeout = timeout
	}
}

func WithWriteTimeout(timeout time.Duration) DialOption {
	return func(cfg *DialConfig) {
		cfg.WriteTimeout = timeout
	}
}

func WithJa3(ja3, userAgent string) DialOption {
	return func(o *DialConfig) {
		o.Ja3.Ja3 = ja3
		o.Ja3.UserAgent = userAgent
	}
}

func WithProxyURL(proxyURL string) DialOption {
	return func(o *DialConfig) {
		o.ProxyURL = proxyURL
	}
}

func WithDialer(dialer proxy.Dialer) DialOption {
	return func(o *DialConfig) {
		o.dialer = dialer
	}
}

// 修改Config.json的路径
func WithConfigPrefixName(prefixName string) Option {
	return func(cfg *Config) {
		// 注销配置
		config.Unregister(cfg)

		cfg.PrefixName = prefixName

		// 重新注册
		config.Register(cfg)
	}
}
