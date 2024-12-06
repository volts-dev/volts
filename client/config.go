package client

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/config"
	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/selector"
	"github.com/volts-dev/volts/transport"
)

var (
	log = logger.New("Client")

	// Default Client
	defaultClient IClient
	// DefaultRetry is the default check-for-retry function for retries
	DefaultRetry = RetryOnError
	// DefaultRetries is the default number of times a request is tried
	DefaultRetries = 1
	// DefaultRequestTimeout is the default request timeout
	DefaultRequestTimeout = time.Second * 20
	// DefaultPoolSize sets the connection pool size
	DefaultPoolSize = 100
	// DefaultPoolTTL sets the connection pool ttl
	DefaultPoolTTL = time.Minute
)

type (
	RequestOptions struct {
		ContentType   string
		Stream        bool
		Codec         codec.ICodec
		Encoded       bool // 传入的数据已经是编码器过的
		SerializeType codec.SerializeType

		Service string // 为该请求指定微服务名称
		// Request/Response timeout
		RequestTimeout time.Duration

		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context
	}

	CallOptions struct {
		SelectOptions []selector.SelectOption

		// Address of remote hosts
		Address []string
		// Backoff func
		//Backoff BackoffFunc
		// Check if retriable func
		Retry RetryFunc
		// Number of Call attempts
		Retries int
		// Transport Dial Timeout
		DialTimeout time.Duration
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

	HttpOption func(*Config)
	// Option contains all options for creating clients.
	Option func(*Config)
	// CallOption used by Call or Stream
	CallOption func(*CallOptions)

	Config struct {
		config.Config `field:"-"`
		Name          string `field:"-"` // config name/path in config file
		PrefixName    string `field:"-"` // config prefix name
		// Other options for implementations of the interface
		// can be stored in a context
		Logger      logger.ILogger         `field:"-"` // 保留:提供给扩展使用
		Context     context.Context        `field:"-"`
		Client      IClient                `field:"-"`
		Transport   transport.ITransport   `field:"-"`
		Registry    registry.IRegistry     `field:"-"`
		Selector    selector.ISelector     `field:"-"`
		TlsConfig   *tls.Config            `field:"-"` // TLSConfig for tcp and quic
		CallOptions CallOptions            `field:"-"` // Default Call Options
		DialOptions []transport.DialOption `field:"-"` // TODO 		// 提供实时变化sturct
		// Breaker is used to config CircuitBreaker
		///Breaker Breaker

		// Connection Pool
		PoolSize int
		PoolTtl  time.Duration
		Retries  int // Retries retries to send

		// Used to select codec
		ContentType string
		_conn       net.Conn
		_protocol   string

		// Group is used to select the services in the same group. Services set group info in their meta.
		// If it is empty, clients will ignore group.
		Group string

		// kcp.BlockCrypt
		_Block interface{}
		// RPCPath for http connection
		_RPCPath string

		// BackupLatency is used for Failbackup mode. rpc will sends another request if the first response doesn't return in BackupLatency time.
		BackupLatency time.Duration

		// 传输序列类型
		Serialize     codec.SerializeType `field:"-"`
		SerializeType string              //codec.SerializeType
		CompressType  transport.CompressType

		Heartbeat         bool
		HeartbeatInterval time.Duration

		Ja3      transport.Ja3 `field:"-"`
		ProxyUrl string
		// http options
		UserAgent     string
		AllowRedirect bool

		// Debug mode
		PrintRequest    bool
		PrintRequestAll bool

		// Registry
		RegistryType string
		RegistryHost string

		// Selector
		SelectorStrategy string
	}

	// RequestOption used by NewRequest
	RequestOption func(*RequestOptions)
)

func newConfig(tr transport.ITransport, opts ...Option) *Config {
	cfg := &Config{
		Name: "client",
		//Config:    config.Default(),
		Logger:    log,
		Transport: tr,
		Retries:   3,
		//RPCPath:        share.DefaultRPCPath,
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
		PoolTtl:  DefaultPoolTTL,
		//Broker:    broker.DefaultBroker,
		//Selector: selector.Default(),
		//Registry: registry.Default(),
		SelectorStrategy: "random",
	}

	//if cfg.Name == "" {
	//	cfg.Name = "client"
	//}
	cfg.Init(opts...)

	config.Register(cfg)
	/*
		// 保存/加载
		if !config.Default().InConfig(cfg.String()) {
			// 保存到内存
			if err := cfg.Save(false); err != nil {
				log.Fatalf("save %v config failed!", cfg.String())
			}
			// 保存到配置文件
			config.Default().Save()
		} else if err := cfg.Load(); err != nil {
			log.Fatalf("load %v config failed!", cfg.String())
		}
	*/
	// 初始化序列
	if st := codec.Use(cfg.SerializeType); st != 0 {
		cfg.Serialize = st
		cfg.SerializeType = st.String()
	}

	// 初始化 transport
	cfg.Transport.Init(transport.WithConfigPrefixName(cfg.String()))

	// 初始化 registry
	reg := registry.Use(cfg.RegistryType, registry.WithConfigPrefixName(cfg.String()), registry.Addrs(cfg.RegistryHost))
	if reg != nil {
		cfg.Registry = reg
		cfg.Selector = selector.Default(
			selector.WithConfigPrefixName(cfg.String()), // 配置路径
			selector.Registry(cfg.Registry),
			selector.WithStrategy(cfg.SelectorStrategy),
		)
	}

	return cfg
}

func (self *Config) String() string {
	if len(self.PrefixName) > 0 {
		return strings.Join([]string{"client", self.PrefixName}, ".")
	}
	return self.Name
}

// init options
func (self *Config) Init(opts ...Option) {
	for _, opt := range opts {
		if opt != nil {
			opt(self)
		}
	}
}

func (self *Config) Load() error {
	return self.LoadToModel(self)
}

func (self *Config) Save(immed ...bool) error {
	return self.SaveFromModel(self, immed...)
}

// 增加超时到60s
func Debug() Option {
	return func(cfg *Config) {
		cfg.Debug = true
		cfg.CallOptions.RequestTimeout = 180 * time.Second
		cfg.CallOptions.DialTimeout = 180 * time.Second
		cfg.Transport.Init(
			transport.DialTimeout(cfg.CallOptions.DialTimeout),
			transport.ReadTimeout(cfg.CallOptions.DialTimeout),
			transport.WriteTimeout(cfg.CallOptions.DialTimeout),
		)
	}
}

func Logger() logger.ILogger {
	return log
}

func WithCodec(c codec.SerializeType) RequestOption {
	return func(cfg *RequestOptions) {
		cfg.SerializeType = c
		cfg.Codec = codec.IdentifyCodec(c)
	}
}

func Encoded(on bool) RequestOption {
	return func(cfg *RequestOptions) {
		cfg.Encoded = on
	}
}

func WithServiceName(name string) RequestOption {
	return func(cfg *RequestOptions) {
		cfg.Service = name
	}
}

func WithRequestTimeout(d time.Duration) RequestOption {
	return func(opts *RequestOptions) {
		opts.RequestTimeout = d
	}
}

// WithDialTimeout is a CallOption which overrides that which
// set in Options.CallOptions
func WithDialTimeout(d time.Duration) CallOption {
	return func(o *CallOptions) {
		o.DialTimeout = d
	}
}

func WithStreamTimeout(d time.Duration) CallOption {
	return func(o *CallOptions) {
		o.StreamTimeout = d
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

func WithUserAgent(userAgent string) HttpOption {
	return func(cfg *Config) {
		cfg.UserAgent = userAgent
		cfg.Ja3.UserAgent = userAgent
	}
}

func AllowRedirect() HttpOption {
	return func(cfg *Config) {
		cfg.AllowRedirect = true
	}
}

// Codec to be used to encode/decode requests for a given content type
func WithSerializeType(st codec.SerializeType) Option {
	return func(cfg *Config) {
		cfg.Serialize = st
		cfg.SerializeType = st.String()
	}
}

// Registry to find nodes for a given service
func WithRegistry(r registry.IRegistry) Option {
	return func(cfg *Config) {
		cfg.Registry = r
		// set in the selector
		if cfg.Selector != nil {
			cfg.Selector.Init(selector.Registry(r))
		}
	}
}

func WithRegistries(typ string, Host string) Option {
	return func(cfg *Config) {
		cfg.RegistryType = typ
		cfg.RegistryHost = Host
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
		cfg.UserAgent = userAgent
		//	cfg.DialOptions = append(cfg.DialOptions, transport.WithJa3(cfg.Ja3.Ja3, cfg.Ja3.UserAgent))
	}
}

func WithProxyURL(proxyURL string) Option {
	return func(cfg *Config) {
		cfg.ProxyUrl = proxyURL
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

// 打印请求信息
func WithPrintRequest(all ...bool) Option {
	return func(cfg *Config) {
		cfg.PrintRequest = true
		if len(all) > 0 {
			cfg.PrintRequestAll = all[0]
		}
	}
}

// 固定服务器列表
func WithHost(adr ...string) Option {
	return func(cfg *Config) {
		cfg.CallOptions.Address = append(cfg.CallOptions.Address, adr...)
	}
}

// 修改Config.json的路径
func WithConfigPrefixName(prefixName string) Option {
	return func(cfg *Config) {
		cfg.Unregister(cfg)
		cfg.PrefixName = prefixName
		cfg.Register(cfg)
	}
}
