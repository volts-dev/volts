package client

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"

	"github.com/volts-dev/volts/codec"
	log "github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/selector"
	"github.com/volts-dev/volts/transport"
)

var logger log.ILogger = log.NewLogger(log.WithPrefix("Client"))

type (
	RequestOptions struct {
		ContentType   string
		Stream        bool
		Codec         codec.ICodec
		SerializeType codec.SerializeType
		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context
	}
	HttpOption func(*Config)
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
		Client    IClient
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

		// http options
		userAgent     string
		allowRedirect bool
	}

	// RequestOption used by NewRequest
	RequestOption func(*RequestOptions)
)

func newConfig(tr transport.ITransport, opts ...Option) *Config {
	cfg := &Config{
		Transport: tr,
		logger:    logger,
		Retries:   3,
		//RPCPath:        share.DefaultRPCPath,
		ConnectTimeout: 10 * time.Second,
		//SerializeType:  codec.MsgPack,
		CompressType:  transport.None,
		BackupLatency: 10 * time.Millisecond,

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
		//Selector: selector.Default(),
		//Registry: registry.Default(),
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

func __WithContentType(c string) RequestOption {
	return func(cfg *RequestOptions) {
		cfg.ContentType = c
	}
}

func WithCodec(c codec.SerializeType) RequestOption {
	return func(cfg *RequestOptions) {
		cfg.SerializeType = c
		cfg.Codec = codec.IdentifyCodec(c)
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

func WithContext(ctx context.Context) CallOption {
	return func(o *CallOptions) {
		o.Context = ctx
	}
}

func WithCookiejar(jar http.CookieJar) HttpOption {
	return func(cfg *Config) {
		if cli, ok := cfg.Client.(*HttpClient); ok {
			cli.client.Jar = jar
		}
	}
}

func Ua(userAgent string) HttpOption {
	return func(cfg *Config) {
		cfg.userAgent = userAgent
	}
}

func AllowRedirect() HttpOption {
	return func(cfg *Config) {
		cfg.allowRedirect = true
	}
}

// Codec to be used to encode/decode requests for a given content type
func WithSerializeType(c codec.SerializeType) Option {
	return func(cfg *Config) {
		cfg.SerializeType = c
	}
}

// Registry to find nodes for a given service
func WithRegistry(r registry.IRegistry) Option {
	return func(cfg *Config) {
		cfg.Registry = r
		// set in the selector
		cfg.Selector.Init(selector.Registry(r))
	}
}

// Transport to use for communication e.g http, rabbitmq, etc
func WithTransport(t transport.ITransport) Option {
	return func(cfg *Config) {
		cfg.Transport = t
	}
}
func WithJa3(ja3, userAgent string) Option {
	return func(cfg *Config) {
		cfg.Ja3.Ja3 = ja3
		cfg.Ja3.UserAgent = userAgent
		//	cfg.DialOptions = append(cfg.DialOptions, transport.WithJa3(cfg.Ja3.Ja3, cfg.Ja3.UserAgent))
	}
}

func WithProxyURL(proxyURL string) Option {
	return func(cfg *Config) {
		cfg.ProxyURL = proxyURL
		//cfg.DialOptions = append(cfg.DialOptions, transport.WithProxyURL(cfg.ProxyURL))
	}
}

// init transport
func WithTransportOptions(opts ...transport.Option) Option {
	return func(cfg *Config) {
		cfg.Transport.Init(opts...)
	}
}

func WithHttpOptions(opts ...HttpOption) Option {
	return func(cfg *Config) {
		for _, opt := range opts {
			if opt != nil {
				opt(cfg)
			}
		}
	}
}
