package registry

import (
	"context"
	"crypto/tls"
	"time"
)

// use a .volts domain rather than .local
var mdnsDomain = "volts"

type (
	Option       func(*Config) error
	WatchOptions func(*WatchConfig) error

	Config struct {
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

// Addrs is the registry addresses to use
func Addrs(addrs ...string) Option {
	return func(cfg *Config) error {
		cfg.Addrs = addrs
		return nil
	}
}

func Timeout(t time.Duration) Option {
	return func(cfg *Config) error {
		cfg.Timeout = t
		return nil
	}
}

// Secure communication with the registry
func Secure(b bool) Option {
	return func(cfg *Config) error {
		cfg.Secure = b
		return nil
	}
}

// Specify TLS Config
func TLSConfig(t *tls.Config) Option {
	return func(cfg *Config) error {
		cfg.TLSConfig = t
		return nil
	}
}

func RegisterTTL(t time.Duration) Option {
	return func(o *Config) error {
		o.TTL = t
		return nil
	}
}
