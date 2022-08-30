package pool

import (
	"time"

	"github.com/volts-dev/volts/transport"
)

type Config struct {
	Transport transport.ITransport
	TTL       time.Duration
	Size      int
}

type Option func(*Config)

func Size(i int) Option {
	return func(o *Config) {
		o.Size = i
	}
}

func Transport(t transport.ITransport) Option {
	return func(o *Config) {
		o.Transport = t
	}
}

func TTL(t time.Duration) Option {
	return func(o *Config) {
		o.TTL = t
	}
}
