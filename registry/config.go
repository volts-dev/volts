package registry

import (
	"context"
	"crypto/tls"
	"strings"
	"time"

	"github.com/volts-dev/volts/config"
	"github.com/volts-dev/volts/logger"
)

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
		*config.Config `field:"-"`
		Name           string          // config name/path in config file
		PrefixName     string          `field:"-"` // config prefix name
		Logger         logger.ILogger  `field:"-"` // 实例
		Context        context.Context `field:"-"`
		LocalServices  []*Service      `field:"-"` // current service information
		Addrs          []string        `field:"-"`
		Type           string
		Timeout        time.Duration
		Secure         bool
		TlsConfig      *tls.Config
		Ttl            time.Duration `field:"ttl"`
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
		Config:  config.Default(),
		Logger:  log,
		Context: context.Background(),
		Timeout: time.Millisecond * 100,
	}
	cfg.Init(opts...)
	if cfg.Name != "" {
		config.Register(cfg)
	}
	return cfg
}

func (self *Config) String() string {
	if len(self.PrefixName) > 0 {
		return strings.Join([]string{"registry", self.PrefixName}, ".")
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
	if self.Name == "" {
		return nil
	}

	return self.LoadToModel(self)
}

func (self *Config) Save(immed ...bool) error {
	if self.Name == "" {
		return nil
	}

	return self.SaveFromModel(self, immed...)
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
		cfg.Ttl = t
	}
}

// 修改Config.json的路径
func WithName(name string) Option {
	return func(cfg *Config) {
		cfg.Name = name
		// 重新加载
		cfg.Load()
	}
}

// 修改Config.json的路径
func WithConfigPrefixName(prefixName string) Option {
	return func(cfg *Config) {
		cfg.PrefixName = prefixName
		// 重新加载
		cfg.Load()
	}
}
