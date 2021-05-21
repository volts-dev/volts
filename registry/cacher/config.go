package cacher

import (
	"time"
)

type (
	Option func(*Config)

	Config struct {
		// TTL is the cache TTL
		TTL time.Duration
	}
)

// WithTTL sets the cache TTL
func WithTTL(t time.Duration) Option {
	return func(cfg *Config) {
		cfg.TTL = t
	}
}
