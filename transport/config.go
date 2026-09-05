package transport

import (
	"context"
	"crypto/tls"
	"strings"
	"time"

	"github.com/volts-dev/volts/config"
	"github.com/volts-dev/volts/internal/acme"
	"github.com/volts-dev/volts/internal/acme/autocert"
	"github.com/volts-dev/volts/logger"
	"golang.org/x/net/proxy"
)

var (
	log            = logger.New("Transport")
	DefaultTimeout = 15 * time.Second

	// DefaultReadHeaderTimeout 是读完请求头的默认时限。
	// 请求头只有几百字节，20 秒对任何真实客户端都绰绰有余，对 Slowloris 则是致命的。
	DefaultReadHeaderTimeout = 20 * time.Second

	// DefaultIdleTimeout 是 keep-alive 连接的默认空闲时限。
	DefaultIdleTimeout = 120 * time.Second

	// DefaultMaxHeaderBytes 是请求头的默认上限（标准库默认 1MB，对 RPC/Web 都偏大；
	// 8KiB 足以容纳常规 cookie + UA + 自定义头）。
	DefaultMaxHeaderBytes = 8 << 10
)

type (
	Option       func(*Config)
	DialOption   func(*DialConfig)
	ListenOption func(*ListenConfig)

	Config struct {
		config.Config
		Name       string `field:"-"` // config name/path in config file
		PrefixName string `field:"-"` // config prefix name
		Listener   IListener
		// Addrs is the list of intermediary addresses to connect to
		//Addrs []string
		Addrs string

		// Codec is the codec interface to use where headers are not supported
		// by the transport and the entire payload must be encoded
		//Codec codec.Marshaler

		// 证书
		EnableACME   bool          `field:"enable_acme"`
		ACMEHosts    []string      `field:"acme_hosts"`
		ACMEProvider acme.Provider `field:"-"`
		// Secure tells the transport to secure the connection.
		// In the case TLSConfig is not specified best effort self-signed
		// certs should be used
		Secure bool
		// TLSConfig to secure the connection. The assumption is that this
		// is mTLS keypair
		TlsConfig *tls.Config

		// EnableH2C 允许明文连接上的 HTTP/2（h2c）。默认关闭。
		//
		// 此前非 TLS 监听会**无条件**包上 h2c handler。h2c 的明文升级
		// （Upgrade: h2c / PRI * HTTP/2.0）在有前置代理的部署里是请求走私的
		// 经典载体：前端代理按 HTTP/1.1 理解报文、后端按 HTTP/2 帧理解，
		// 两者对"一个请求到哪里结束"产生分歧即可跨请求注入。
		// 直接对外或经 gRPC 网关时才应打开。
		EnableH2C bool `field:"enable_h2c"`

		//DialTimeout sets timeout for dialing
		DialTimeout time.Duration
		// ReadTimeout sets readdeadline for underlying net.Conns
		ReadTimeout time.Duration
		// WriteTimeout sets writedeadline for underlying net.Conns
		WriteTimeout time.Duration

		// ReadHeaderTimeout 是**读完请求头**的时限，与 ReadTimeout 分开。
		//
		// 分开是因为二者的正当取值天差地别：ReadTimeout 覆盖整个请求（一个 2GB 的
		// 上传就该允许它很长），而收几百字节的请求头无论如何不该花上几分钟。合成
		// 一个的后果是，任何为了正当理由把 ReadTimeout 调大的部署，都顺带把"慢速
		// 发送请求头"这条路一起放开了 —— 攻击者每隔几十秒发一个字节，一条连接就
		// 长期占着一个 goroutine 与一个 fd（Slowloris）。
		//
		// **Debug() 故意不动它**：调试器断点停在的是 handler，那已经在请求头读完
		// 之后；没有任何正当理由需要几分钟来收请求头。
		ReadHeaderTimeout time.Duration `field:"read_header_timeout"`

		// IdleTimeout 是 keep-alive 连接上**两次请求之间**的空闲时限。
		//
		// 不设时标准库回落到 ReadTimeout —— 于是 ReadTimeout 一调大，空闲连接也跟着
		// 被留很久，一样是拿 fd 换不了任何东西。它不影响正在进行的请求（SSE、长轮询
		// 都是活跃请求，不算空闲），也不影响已被接管的连接（WebSocket 升级后脱离
		// http.Server 管理）。
		IdleTimeout time.Duration `field:"idle_timeout"`

		// MaxHeaderBytes 是请求头的字节上限。标准库默认 1MB，对 RPC/Web 都偏大。
		MaxHeaderBytes int `field:"max_header_bytes"`

		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context
	}

	DialConfig struct {
		// Tells the transport this is a streaming connection with
		// multiple calls to send/recv and that send may not even be called
		Stream bool
		// Other options for implementations of the interface
		// can be stored in a context
		Secure      bool
		DialTimeout time.Duration
		// ReadTimeout sets readdeadline for underlying net.Conns
		ReadTimeout time.Duration
		// WriteTimeout sets writedeadline for underlying net.Conns
		WriteTimeout time.Duration

		// TODO: add tls options when dialling
		// Currently set in global options
		Ja3      Ja3 // TODO 添加加缓存
		ProxyURL string
		dialer   proxy.Dialer
		Network  string

		Context context.Context
	}

	ListenConfig struct {
		// TODO: add tls options when listening
		// Currently set in global options

		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context
	}
)

func (self *DialConfig) Init(opts ...DialOption) {
	for _, opt := range opts {
		if opt != nil {
			opt(self)
		}
	}
}

func newConfig(opts ...Option) *Config {
	cfg := &Config{
		Name:              "default",
		DialTimeout:       DefaultTimeout,
		ReadTimeout:       DefaultTimeout,
		WriteTimeout:      DefaultTimeout,
		ReadHeaderTimeout: DefaultReadHeaderTimeout,
		IdleTimeout:       DefaultIdleTimeout,
		MaxHeaderBytes:    DefaultMaxHeaderBytes,
	}
	cfg.Init(opts...)
	config.Register(cfg)
	return cfg
}

func (self *Config) String() string {
	if len(self.PrefixName) > 0 {
		return strings.Join([]string{self.PrefixName, "transport"}, ".")
	}

	if len(self.Name) > 0 {
		return strings.Join([]string{"transport", self.Name}, ".")
	}

	return self.Name
}

func (self *Config) Init(opts ...Option) {
	for _, opt := range opts {
		if opt != nil {
			opt(self)
		}
	}
}

// OnReload 声明传输层配置不支持热重载。
//
// 这一节里几乎每一项改了都要重启才可能生效：Addrs / TlsConfig / Secure / EnableACME
// 决定 listener 怎么建，而 listener 在启动时就已经建好了；改配置文件不会让一个已经
// 在 :35999 上 Accept 的监听器换到别的端口。
//
// ReadTimeout / WriteTimeout 理论上可以热更（每条连接读一次），但它们同时是整个
// 框架里被读得最多的配置字段（生产代码里 19 处），热更收益是「改超时不用重启」，
// 代价是这 19 个读点每一个都与重载写入存在数据竞态。改超时本就是运维动作，重启
// 完全可以接受，所以这里整节退出热重载 —— 竞态直接归零，而不是收窄。
//
// 被忽略的变更由框架逐条打印并提示需重启，不会无声无息（见 config/reload.go）。
func (self *Config) OnReload(next config.IConfig) error {
	return config.ErrReloadUnsupported
}

func (self *Config) Load() error {
	if err := self.Config.Load(); err != nil {
		return err
	}

	// 打开了SSL需要指定自动更新服务者
	if self.EnableACME && self.ACMEProvider == nil {
		// 默认是 Let’s Encrypt
		self.ACMEProvider = autocert.NewProvider()
	}

	return nil
}

// Debug 把读写与拨号超时放到近乎无限，好让调试器在断点上停多久都不会被掐断。
//
// **它故意不动 ReadHeaderTimeout / IdleTimeout。** 断点停在 handler 里，那已经是
// 请求头读完之后的事；把"收请求头"也放到 999 秒，等于对每一个开着 Debug 的进程
// 敞开 Slowloris —— 而 vectors 的 cmd.Start() 是**无条件**调 volts.Debug() 的，
// 也就是说线上跑的每一个进程都会走到这里。
func Debug() Option {
	return func(cfg *Config) {
		cfg.Debug = true
		cfg.ReadTimeout = 999 * time.Second
		cfg.WriteTimeout = 999 * time.Second
		cfg.DialTimeout = 999 * time.Second
	}
}

// WithReadHeaderTimeout 设定读完请求头的时限，见 Config.ReadHeaderTimeout。
// <=0 表示不限（不建议：那正是 Slowloris 想要的）。
func WithReadHeaderTimeout(t time.Duration) Option {
	return func(cfg *Config) {
		cfg.ReadHeaderTimeout = t
	}
}

// WithIdleTimeout 设定 keep-alive 空闲时限，见 Config.IdleTimeout。
func WithIdleTimeout(t time.Duration) Option {
	return func(cfg *Config) {
		cfg.IdleTimeout = t
	}
}

// WithMaxHeaderBytes 设定请求头字节上限。<=0 时回落到 DefaultMaxHeaderBytes。
func WithMaxHeaderBytes(n int) Option {
	return func(cfg *Config) {
		cfg.MaxHeaderBytes = n
	}
}

func Logger() logger.ILogger {
	return log
}

// Addrs to use for transport
func WithAddrs(addrs string) Option {
	return func(cfg *Config) {
		cfg.Addrs = addrs
	}
}

// Timeout sets the timeout for Send/Recv execution
func Timeout(t time.Duration) Option {
	return func(cfg *Config) {
		cfg.ReadTimeout = t
		cfg.WriteTimeout = t
		cfg.DialTimeout = t
	}
}

// Timeout sets the timeout for Send/Recv execution
func ReadTimeout(t time.Duration) Option {
	return func(cfg *Config) {
		cfg.ReadTimeout = t
	}
}

// Timeout sets the timeout for Send/Recv execution
func WriteTimeout(t time.Duration) Option {
	return func(cfg *Config) {
		cfg.WriteTimeout = t
	}
}

// Timeout sets the timeout for Send/Recv execution
func DialTimeout(t time.Duration) Option {
	return func(cfg *Config) {
		cfg.DialTimeout = t
	}
}

func EnableACME(b bool) Option {
	return func(cfg *Config) {
		cfg.EnableACME = b
	}
}
func ACMEHosts(hosts ...string) Option {
	return func(o *Config) {
		o.ACMEHosts = hosts
	}
}

func ACMEProvider(p acme.Provider) Option {
	return func(o *Config) {
		o.ACMEProvider = p
	}
}

// Use secure communication. If TLSConfig is not specified the default
// tls.Config{} is used (peer certificate verification enabled). To skip
// verification (self-signed test certs, etc.), pass an explicit
// TLSConfig(&tls.Config{InsecureSkipVerify: true}).
func Secure(b bool) Option {
	return func(cfg *Config) {
		cfg.Secure = b
	}
}

// TLSConfig to be used for the transport.
func TLSConfig(t *tls.Config) Option {
	return func(cfg *Config) {
		cfg.TlsConfig = t
	}
}

// EnableH2C 开启明文连接上的 HTTP/2（h2c）。默认关闭。
//
// 只在服务直接对外、或确实需要明文 HTTP/2（如 gRPC 网关）时开启。
// 若前面还有 Nginx/ALB 之类的 HTTP/1.1 代理，开启它会引入请求走私风险
// —— 前后端对"请求边界"的理解不一致即可被跨请求注入。
func EnableH2C(b bool) Option {
	return func(cfg *Config) {
		cfg.EnableH2C = b
	}
}

func WithNetwork(network string) DialOption {
	return func(o *DialConfig) {
		o.Network = network
	}
}

func WithTLS() DialOption {
	return func(o *DialConfig) {
		o.Secure = true
	}
}

func WithContext(ctx context.Context) DialOption {
	return func(o *DialConfig) {
		o.Context = ctx
	}
}

// Indicates whether this is a streaming connection
func WithStream() DialOption {
	return func(o *DialConfig) {
		o.Stream = true
	}
}

func WithTimeout(dial, read, write time.Duration) DialOption {
	return func(cfg *DialConfig) {
		if dial > 0 {
			cfg.DialTimeout = dial
		}
		if read > 0 {
			cfg.ReadTimeout = read
		}
		if write > 0 {
			cfg.WriteTimeout = write
		}
	}
}

func WithDialTimeout(timeout time.Duration) DialOption {
	return func(cfg *DialConfig) {
		cfg.DialTimeout = timeout
	}
}

func WithReadTimeout(timeout time.Duration) DialOption {
	return func(cfg *DialConfig) {
		cfg.ReadTimeout = timeout
	}
}

func WithWriteTimeout(timeout time.Duration) DialOption {
	return func(cfg *DialConfig) {
		cfg.WriteTimeout = timeout
	}
}

func WithJa3(ja3, userAgent string) DialOption {
	return func(o *DialConfig) {
		o.Ja3.Ja3 = ja3
		o.Ja3.UserAgent = userAgent
	}
}

func WithProxyURL(proxyURL string) DialOption {
	return func(o *DialConfig) {
		o.ProxyURL = proxyURL
	}
}

func WithDialer(dialer proxy.Dialer) DialOption {
	return func(o *DialConfig) {
		o.dialer = dialer
	}
}

// 修改Config.json的路径
func WithConfigPrefixName(prefixName string) Option {
	return func(cfg *Config) {
		// 注销配置
		config.Unregister(cfg)
		cfg.PrefixName = prefixName
		// 重新注册
		config.Register(cfg)
	}
}
