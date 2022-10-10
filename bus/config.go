package bus

import (
	"context"
	"crypto/tls"
	"time"

	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/config"
	"github.com/volts-dev/volts/registry"
)

type (
	Option          func(*Config)
	PublishOption   func(*PublishConfig)
	SubscribeOption func(*SubscribeConfig)

	Config struct {
		*config.Config

		Addrs []string

		// Handler executed when error happens in broker mesage
		// processing
		ErrorHandler Handler
		Secure       bool
		TLSConfig    *tls.Config
		// Codec
		Codec codec.ICodec
		// Registry used for clustering
		Registry registry.IRegistry
		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context
	}

	PublishConfig struct {
		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context
	}

	SubscribeConfig struct {
		// AutoAck defaults to true. When a handler returns
		// with a nil error the message is acked.
		AutoAck bool
		// Subscribers with the same queue name
		// will create a shared subscription where each
		// receives a subset of messages.
		Queue string

		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context
	}
)

var (
	DefaultBus IBus = NewHttpBus()

	DefaultPath      = "/"
	DefaultAddress   = "127.0.0.1:0"
	serviceName      = "micro.http.broker"
	broadcastVersion = "ff.http.broadcast"
	registerTTL      = time.Minute
	registerInterval = time.Second * 30
)

func NewSubscribeOptions(opts ...SubscribeOption) *SubscribeConfig {
	cfg := &SubscribeConfig{
		AutoAck: true,
	}

	for _, opt := range opts {
		opt(cfg)
	}

	return cfg
}
