package cacher

import (
	"time"

	"github.com/volts-dev/volts/logger"
)

type (
	Option func(*Config)

	Config struct {
		// TTL is the cache TTL
		TTL time.Duration
	}
)

var log logger.ILogger = logger.New("Registry.Cacher")

// WithTTL sets the cache TTL
func WithTTL(t time.Duration) Option {
	return func(cfg *Config) {
		cfg.TTL = t
	}
}
