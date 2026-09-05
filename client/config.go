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
	// DefaultRetry 是默认的重试判定。默认从不重试 —— 见 RetryNever 的说明：
	// 重试一个非幂等调用等于让它执行两次，框架不替调用方做这个决定。
	//
	// 历史上这里是 RetryOnError，但那个函数一直是死的（取不到状态码，恒返回 false），
	// 所以"默认不重试"其实是既有的**实际**行为，这里只是把它写成事实。
	DefaultRetry = RetryNever
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

		// 曾有一个 `CallWrappers []CallWrapper` 的半成品挂在这里（类型从未定义、
		// 调用点也是注释掉的）。它想包住两个 client 各自的低层 call，而那两个函数返回
		// 不同的具体类型（*httpResponse / *rpcResponse），一个类型套不住；且横切信息
		// 在重试循环内部重复施加没有意义。改成请求级钩子挂在两条链路共用的 IRequest
		// 上（见 hook.go 的 RequestHook），问题自然消失。

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
		// RequestHooks 本客户端的出站请求钩子，在选节点与重试之前逐个执行（见 hook.go）。
		// 进程级钩子（RegisterGlobalRequestHook）先于它们执行。
		RequestHooks []RequestHook `field:"-"`
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
		RegistryHost []string

		// Selector
		SelectorStrategy string
	}

	// RequestOption used by NewRequest
	RequestOption func(*RequestOptions)
)

func newConfig(tr transport.ITransport, opts ...Option) *Config {
	cfg := &Config{
		Name:      "default",
		Logger:    log,
		Transport: tr,
		// 与 CallOptions.Retries 保持一致。此前这里写死 3 而**没有任何读取方**，
		// 于是 config.json 里那条 client.*.retries 是个改了不生效的旋钮 ——
		// 正是"改了配置没反应"最贵的那一类。现在它被 newConfig 末尾同步进
		// CallOptions，真正起作用。
		Retries: DefaultRetries,
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

	// 初始化 registry。
	//
	// **调用方显式给了实例就用它，绝不按 RegistryHost 再解析一遍。**
	// RegistryHost 存在共享的配置路径 `client.default` 下（Config.String() 只按
	// Name/PrefixName 取路径，而绝大多数客户端都用默认名），于是同进程里**先**创建
	// 的那个客户端写进去的地址，会被**后**创建的每一个同路径客户端继承——哪怕后者
	// 明确用 WithRegistry/WithEtcdRegistry 传了另一个实例进来。
	//
	// 症状是静默连错注册中心：2026-08-06 实测，一个进程用 VECTORS_ETCD_ADDRS 指到
	// 127.0.0.1:12379 起了整套隔离环境，registry 实例也确实绑在 12379 上，但这行
	// 把它换成了按 RegistryHost=["127.0.0.1"]（缺端口→默认 2379）解析出来的那个，
	// 于是 /api/load_views 这类内部 RPC 全部打到了**另一套环境**的进程上。不报错、
	// 有正常响应（同一个库），只是答话的是别人的二进制——查了很久才定位到这里。
	reg := cfg.Registry
	if reg == nil {
		reg = registry.Use(cfg.RegistryType, registry.Addrs(cfg.RegistryHost...))
	}
	if reg != nil {
		cfg.Registry = reg
		// 回填成实例的真实地址：RegistryHost 是给配置/诊断看的，让它继续显示一个
		// 与真实连接不符的值，就是下一次同样难查的起点。
		cfg.RegistryHost = reg.Config().Addrs
		cfg.Selector = selector.New(
			selector.WithConfigPrefixName(cfg.String()), // 配置路径
			selector.Registry(cfg.Registry),
			selector.WithStrategy(cfg.SelectorStrategy),
		)
	}

	// Retries 的单一来源是 Config.Retries（config.json 里的 client.*.retries）。
	// 配置文件里的值经 config.Register → Load 落进 cfg.Retries，这里再同步进真正
	// 被重试循环读取的 CallOptions —— 少了这一步，那条配置项就是个改了不生效的旋钮。
	if cfg.Retries < 1 {
		cfg.Retries = DefaultRetries
	}
	cfg.CallOptions.Retries = cfg.Retries

	return cfg
}

func (self *Config) String() string {
	if len(self.PrefixName) > 0 {
		return strings.Join([]string{self.PrefixName, "client"}, ".")
	}

	if len(self.Name) > 0 {
		return strings.Join([]string{"client", self.Name}, ".")
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

	self.Save()
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
		cfg.RegistryType = r.Config().Name
		cfg.RegistryHost = r.Config().Addrs
		// set in the selector
		if cfg.Selector != nil {
			cfg.Selector.Init(selector.Registry(r))
		}
	}
}

func WithRegistries(typ string, Hosts ...string) Option {
	return func(cfg *Config) {
		cfg.Init(WithRegistry(registry.Use(typ, registry.Addrs(Hosts...))))
		cfg.RegistryType = typ
		cfg.RegistryHost = Hosts
	}
}

// WithRequestHooks 给本客户端追加出站请求钩子（见 hook.go 的 RequestHook）。
// 进程级钩子用 RegisterGlobalRequestHook —— 服务身份那类"一个进程只有一份"的东西
// 应当走进程级，否则每处 New 客户端都得记得传，漏一处就是一次匿名调用。
func WithRequestHooks(hooks ...RequestHook) Option {
	return func(cfg *Config) {
		cfg.RequestHooks = append(cfg.RequestHooks, hooks...)
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
func WithHost(address ...string) Option {
	return func(cfg *Config) {
		cfg.CallOptions.Address = append(cfg.CallOptions.Address, address...)
	}
}

// 修改Config.json的路径
// WithRetries 设定一次调用最多尝试几次（含第一次）。1 = 不重试。
//
// 此前没有任何办法改这个值：CallOptions.Retries 恒为 DefaultRetries，而 Config.Retries
// 没有读取方。**调大它之前先选好策略** —— 默认策略是 RetryNever，光调大次数不会有
// 第二次尝试。写路径请配 RetryOnConnectError（只在连接没建成时重试，可证明不会重复
// 执行），只有确认幂等的读路径才适合 RetryOnError。
func WithRetries(n int) Option {
	return func(cfg *Config) {
		if n < 1 {
			n = 1
		}
		cfg.Retries = n
		cfg.CallOptions.Retries = n
	}
}

// WithRetry 指定重试判定函数，见 RetryNever / RetryOnConnectError / RetryOnError。
func WithRetry(fn RetryFunc) Option {
	return func(cfg *Config) {
		if fn != nil {
			cfg.CallOptions.Retry = fn
		}
	}
}

func WithConfigPrefixName(prefixName string) Option {
	return func(cfg *Config) {
		cfg.Unregister(cfg)
		cfg.PrefixName = prefixName
		cfg.Register(cfg)
	}
}
