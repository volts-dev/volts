package registry

import (
	"context"
	"crypto/tls"
	"strings"
	"time"

	"github.com/volts-dev/volts/config"
	"github.com/volts-dev/volts/logger"
)

type servicesKey struct{}

type (
	Option       func(*Config)
	WatchOptions func(*WatchConfig) error

	RegisterConfig struct {
		TTL time.Duration
		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context
	}

	DeregisterConfig struct {
		Context context.Context
	}

	GetConfig struct {
		Context context.Context
	}

	ListConfig struct {
		Context context.Context
	}

	Config struct {
		config.Config `field:"-"`
		Name          string          `field:"-"` //
		PrefixName    string          `field:"-"` // config prefix name/path in config file
		Logger        logger.ILogger  `field:"-"` // 实例
		Context       context.Context `field:"-"`
		Addrs         []string        `field:"-"`
		TlsConfig     *tls.Config     `field:"-"`

		Timeout time.Duration
		Secure  bool
		TTL     time.Duration `field:"ttl"`
	}

	WatchConfig struct {
		// Specify a service to watch
		// If blank, the watch is for all services
		Service string
		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context
	}
)

// new and init a config
func NewConfig(opts ...Option) *Config {
	cfg := &Config{
		Name:    "default",
		Logger:  log,
		Context: context.Background(),
		Timeout: time.Millisecond * 100,
	}
	cfg.Init(opts...)
	config.Register(cfg)
	return cfg
}

func (self *Config) String() string {
	if len(self.PrefixName) > 0 {
		return strings.Join([]string{self.PrefixName, "registry"}, ".")
	}

	if len(self.Name) > 0 {
		return strings.Join([]string{"registry", self.Name}, ".")
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

func Logger() logger.ILogger {
	return log
}

func Debug() Option {
	return func(cfg *Config) {
		cfg.Debug = true
	}
}

// Addrs is the registry addresses to use
func Addrs(addrs ...string) Option {
	return func(cfg *Config) {
		cfg.Addrs = addrs
	}
}

func Timeout(t time.Duration) Option {
	return func(cfg *Config) {
		cfg.Timeout = t
	}
}

// Secure communication with the registry
func Secure(b bool) Option {
	return func(cfg *Config) {
		cfg.Secure = b
	}
}

// Specify TLS Config
func TLSConfig(t *tls.Config) Option {
	return func(cfg *Config) {
		cfg.TlsConfig = t
	}
}

func RegisterTTL(t time.Duration) Option {
	return func(cfg *Config) {
		cfg.TTL = t
	}
}

// change registry name
func WithName(name string) Option {
	return func(cfg *Config) {
		//cfg.Unregister(cfg)
		cfg.Name = name
		//cfg.Register(cfg)
	}
}

// WithConfigPrefixName changes the prefix name, unregisters the old and registers the new
func WithConfigPrefixName(prefixName string) Option {
	return func(cfg *Config) {
		cfg.Unregister(cfg)
		cfg.PrefixName = prefixName
		cfg.Register(cfg)
	}
}
