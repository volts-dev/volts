package transport

import (
	"context"
	"crypto/tls"
	"time"

	"github.com/volts-dev/volts/config"
	"github.com/volts-dev/volts/logger"
	"golang.org/x/net/proxy"
)

var (
	log            = logger.New("Transport")
	DefaultTimeout = time.Second * 5
)

type (
	Option       func(*Config)
	DialOption   func(*DialConfig)
	ListenOption func(*ListenConfig)

	Config struct {
		*config.Config
		Listener IListener
		// Addrs is the list of intermediary addresses to connect to
		Addrs []string
		// Codec is the codec interface to use where headers are not supported
		// by the transport and the entire payload must be encoded
		//Codec codec.Marshaler
		// Secure tells the transport to secure the connection.
		// In the case TLSConfig is not specified best effort self-signed
		// certs should be used
		Secure bool
		// TLSConfig to secure the connection. The assumption is that this
		// is mTLS keypair
		TLSConfig *tls.Config

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

func newConfig(opts ...Option) *Config {
	cfg := &Config{
		DialTimeout:  DefaultTimeout,
		ReadTimeout:  DefaultTimeout,
		WriteTimeout: DefaultTimeout,
	}

	config.Default().Register(cfg)
	cfg.Init(opts...)
	return cfg
}

func (self *Config) String() string {
	return "transport"
}

func (self *Config) Init(opts ...Option) {
	for _, opt := range opts {
		if opt != nil {
			opt(self)
		}
	}
}

func (self *Config) Load() error {
	return self.LoadToModel(self)
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
		cfg.TLSConfig = t
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
