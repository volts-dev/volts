package client

import (
	"context"
	"crypto/tls"
	"net"
	"time"

	log "github.com/volts-dev/logger"
	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/selector"
	"github.com/volts-dev/volts/transport"
)

var logger log.ILogger = log.NewLogger(log.WithPrefix("Client"))

type (
	RequestOptions struct {
		ContentType string
		Stream      bool

		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context
	}

	// Option contains all options for creating clients.
	Option func(*Config)
	// CallOption used by Call or Stream
	CallOption func(*CallOptions)

	CallOptions struct {
		SelectOptions []selector.SelectOption

		// Address of remote hosts
		Address []string
		// Backoff func
		//Backoff BackoffFunc
		// Check if retriable func
		Retry RetryFunc
		// Transport Dial Timeout
		DialTimeout time.Duration
		// Number of Call attempts
		Retries int
		// Request/Response timeout
		RequestTimeout time.Duration
		// Stream timeout for the stream
		StreamTimeout time.Duration
		// Use the services own auth token
		ServiceToken bool
		// Duration to cache the response for
		CacheExpiry time.Duration

		// Middleware for low level call func
		//CallWrappers []CallWrapper

		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context
	}

	Config struct {
		Transport transport.ITransport
		logger    log.ILogger

		// Connection Pool
		PoolSize    int
		PoolTTL     time.Duration
		Retries     int         // Retries retries to send
		CallOptions CallOptions // Default Call Options
		// 提供实时变化
		DialOptions []transport.DialOption // TODO sturct

		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context

		// Used to select codec
		ContentType string

		Registry registry.IRegistry
		Selector selector.ISelector

		conn     net.Conn
		protocol string

		// Group is used to select the services in the same group. Services set group info in their meta.
		// If it is empty, clients will ignore group.
		Group string

		// TLSConfig for tcp and quic
		TLSConfig *tls.Config

		Ja3      transport.Ja3
		ProxyURL string

		// kcp.BlockCrypt
		Block interface{}
		// RPCPath for http connection
		RPCPath string
		//ConnectTimeout sets timeout for dialing
		ConnectTimeout time.Duration
		// ReadTimeout sets readdeadline for underlying net.Conns
		ReadTimeout time.Duration
		// WriteTimeout sets writedeadline for underlying net.Conns
		WriteTimeout time.Duration

		// BackupLatency is used for Failbackup mode. rpc will sends another request if the first response doesn't return in BackupLatency time.
		BackupLatency time.Duration

		// Breaker is used to config CircuitBreaker
		///Breaker Breaker

		SerializeType codec.SerializeType
		CompressType  transport.CompressType

		Heartbeat         bool
		HeartbeatInterval time.Duration
	}

	// RequestOption used by NewRequest
	RequestOption func(*RequestOptions)
)

func newConfig(opts ...Option) *Config {
	cfg := &Config{
		logger:  logger,
		Retries: 3,
		//RPCPath:        share.DefaultRPCPath,
		ConnectTimeout: 10 * time.Second,
		SerializeType:  codec.MsgPack,
		CompressType:   transport.None,
		BackupLatency:  10 * time.Millisecond,

		CallOptions: CallOptions{
			//Backoff:        DefaultBackoff,
			Retry:          DefaultRetry,
			Retries:        DefaultRetries,
			RequestTimeout: DefaultRequestTimeout,
			DialTimeout:    transport.DefaultTimeout,
		},
		PoolSize: DefaultPoolSize,
		PoolTTL:  DefaultPoolTTL,
		//	Broker:    broker.DefaultBroker,
		Selector: selector.DefaultSelector,
		Registry: registry.DefaultRegistry,
	}

	cfg.Init(opts...)
	return cfg
}

// init options
func (self *Config) Init(opts ...Option) {
	for _, opt := range opts {
		if opt != nil {
			opt(self)
		}
	}
}

func WithContentType(c string) RequestOption {
	return func(cfg *RequestOptions) {
		cfg.ContentType = c
	}
}

// WithRequestTimeout is a CallOption which overrides that which
// set in Options.CallOptions
func WithRequestTimeout(d time.Duration) CallOption {
	return func(o *CallOptions) {
		o.RequestTimeout = d
	}
}

// WithAddress sets the remote addresses to use rather than using service discovery
func WithAddress(a ...string) CallOption {
	return func(o *CallOptions) {
		o.Address = a
	}
}

// Codec to be used to encode/decode requests for a given content type
func WithCodec(c codec.SerializeType) Option {
	return func(cfg *Config) {
		cfg.SerializeType = c
	}
}

// Registry to find nodes for a given service
func Registry(r registry.IRegistry) Option {
	return func(cfg *Config) {
		cfg.Registry = r
		// set in the selector
		cfg.Selector.Init(selector.Registry(r))
	}
}

// Transport to use for communication e.g http, rabbitmq, etc
func Transport(t transport.ITransport) Option {
	return func(cfg *Config) {
		cfg.Transport = t
	}
}
func Ja3(ja3, userAgent string) Option {
	return func(cfg *Config) {
		cfg.Ja3.Ja3 = ja3
		cfg.Ja3.UserAgent = userAgent
		//	cfg.DialOptions = append(cfg.DialOptions, transport.WithJa3(cfg.Ja3.Ja3, cfg.Ja3.UserAgent))
	}
}

func ProxyURL(proxyURL string) Option {
	return func(cfg *Config) {
		cfg.ProxyURL = proxyURL
		//cfg.DialOptions = append(cfg.DialOptions, transport.WithProxyURL(cfg.ProxyURL))
	}
}
