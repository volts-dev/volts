package registry

import (
	"context"
	"crypto/tls"
	"time"

	"github.com/volts-dev/volts/logger"
)

// use a .volts domain rather than .local
var mdnsDomain = "volts"

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
		Service   *Service // current service information
		Addrs     []string
		Timeout   time.Duration
		Secure    bool
		TLSConfig *tls.Config
		// Other options for implementations of the interface
		// can be stored in a context
		TTL time.Duration

		Context context.Context
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
func newConfig() *Config {
	return &Config{
		Context: context.Background(),
		Timeout: time.Millisecond * 100,
	}
}

func Logger() logger.ILogger {
	return log
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
		cfg.TLSConfig = t
	}
}

func RegisterTTL(t time.Duration) Option {
	return func(o *Config) {
		o.TTL = t
	}
}
