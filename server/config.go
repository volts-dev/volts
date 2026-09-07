package server

import (
	"context"
	"crypto/tls"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/volts-dev/utils"
	"github.com/volts-dev/volts/broker"
	"github.com/volts-dev/volts/config"
	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/registry"
	vrouter "github.com/volts-dev/volts/router"
	"github.com/volts-dev/volts/transport"
)

type (
	Option func(*Config)

	// Config 是服务器的配置。纯结构体：导出字段就是配置文件里的项，接口 / 函数类型的
	// 字段（Transport、Router、Registry、Broker、Logger、Context、RegisterCheck）是实例
	// 引用，天然不进配置。配置节是 server.<name>（server.http / server.rpc）。
	//
	// 归属规则：server **拥有**自己的传输层 —— TransportConfig 就是那份配置，随本结构体
	// 一起绑定，配置节 server.<name>.transport.*；router / registry / broker 是跨 server
	// **共享**的实例，只留名字字段（BrokerType / BrokerHost / RegistryType / RegistryHost）
	// 引用它们，它们的配置各自是根节（router.default、registry.etcd、broker.http）。
	Config struct {
		Name  string `field:"-"` // 配置路径 server.<name>
		Debug bool   `field:"-"`
		Uid   string `field:"-"` // 实例

		Address   string
		Advertise string
		Version   string

		// 拥有的传输层的配置（= Transport.Config()）
		TransportConfig *transport.Config `field:"transport"`

		// broker（共享实例的引用）
		BrokerType string
		BrokerHost string

		// Registry（共享实例的引用）
		RegistryType     string
		RegistryHost     []string
		RegisterTTL      time.Duration `field:"register_ttl"`      // The register expiry time
		RegisterInterval time.Duration `field:"register_interval"` // The interval on which to register

		// ---- 实例，不进配置文件 ----
		//Tracer    trace.Tracer
		Subscriber broker.ISubscriber // Subscribe to service name
		Broker     broker.IBroker
		Registry   registry.IRegistry
		Transport  transport.ITransport
		Router     vrouter.IRouter // The router for requests
		Logger     logger.ILogger
		TLSConfig  *tls.Config `field:"-"` // TLSConfig specifies tls.Config for secure serving

		// Other options for implementations of the interface
		// can be stored in a context
		Context       context.Context
		Metadata      map[string]string           `field:"-"`
		RegisterCheck func(context.Context) error // RegisterCheck runs a check function before registering the service
	}
)

var (
	// App settings.
	AppVer      string                             // 程序版本
	AppName     string                             // 名称
	AppUrl      string                             //
	AppSubUrl   string                             //
	AppFilePath string = utils.AppFilePath()       // 可执行程序文件绝对路径
	AppPath     string = filepath.Dir(AppFilePath) // 可执行程序所在文件夹绝对路径
	AppDir      string = filepath.Base(AppPath)    // 文件夹名称

	DefaultAddress          = ":0"
	DefaultName             = "default"
	DefaultVersion          = "latest"
	DefaultRegisterCheck    = func(context.Context) error { return nil }
	DefaultRegisterInterval = time.Second * 30
	DefaultRegisterTTL      = time.Second * 90
	lastStreamResponseError = errors.New("EOS")
)

func newConfig(opts ...Option) *Config {
	cfg := &Config{
		Name:     DefaultName,
		Uid:      uuid.New().String(),
		Logger:   log,
		Metadata: map[string]string{},
		//Address:          DefaultAddress,
		Context:          context.Background(),
		RegisterInterval: DefaultRegisterInterval,
		RegisterTTL:      DefaultRegisterTTL,
		RegisterCheck:    DefaultRegisterCheck,
		Version:          DefaultVersion,
	}
	cfg.Init(opts...)

	// 传输层是 server 拥有的：注入了实例就用它的配置结构体（Bind 会把它从 transport.default
	// 收编到 server.<name>.transport），没注入就先备一份默认配置，实例在装载之后再建 ——
	// 这样文件里的 server.<name>.transport.* 无论哪条路都生效于这台 server 的传输层。
	if cfg.Transport != nil {
		cfg.TransportConfig = cfg.Transport.Config()
	} else {
		cfg.TransportConfig = transport.NewConfig()
	}

	// 一次绑定，整节装载。此后 cfg.Address / RegistryType / BrokerHost … 都是最终值。
	config.Bind("server."+cfg.Name, cfg)

	// 初始化 registry（共享实例，按类型与地址引用）
	if cfg.Registry == nil {
		if reg := registry.Use(cfg.RegistryType, registry.Addrs(cfg.RegistryHost...)); reg != nil {
			cfg.Init(WithRegistry(registry.NewCacher(reg)))
		}
	}

	if cfg.Broker == nil {
		if bk := broker.Use(cfg.BrokerType, broker.Addrs(cfg.BrokerHost)); bk != nil {
			cfg.Init(WithBroker(bk))

			// 把它定为进程默认 broker：只有 server 这份会被注入真 registry
			// （WithRegistry 那条路）并在 Start 里被 Start()。库代码拿的是
			// broker.Default() —— 不认领的话它们拿到的是另一个实例，registry
			// 停在 Nop、端口没监听，发布静默丢消息、订阅收不到任何东西。
			// 调用方要自带 broker，在 volts.New 之后再 SetDefault 一次即可。
			broker.SetDefault(bk)
		}
	}

	// Transport 在此处初始化，晚于 cfg.Init(opts...)。
	// 若用户通过 Address() option 提前设置了 cfg.Address（彼时 Transport 为 nil，
	// Address() 无法同步 Transport.Addrs），此处补偿性地将 cfg.Address 传给 Transport。
	// 注意：config.Bind 已在此前调用，cfg.Address 已是最终值（含配置文件覆盖）。
	if cfg.Transport == nil {
		cfg.Init(WithTransport(transport.NewHTTPTransport(transport.WithConfig(cfg.TransportConfig))))
		addr := cfg.Address
		if addr == "" {
			addr = DefaultAddress
		}
		cfg.Transport.Init(transport.WithAddrs(addr))
	}

	// if not special router use create new
	if cfg.Router == nil {
		cfg.Init(WithRouter(vrouter.New()))
	}

	// Debug 的副作用（transport/router/registry 的调试开关）必须等到上面三者
	// 全部就绪后才能落地：cfg.Init(opts...) 在函数开头执行，彼时 Transport /
	// Router 都还是 nil，Debug() option 只登记 cfg.Debug 与 Address。此处补一次
	// 无 opt 的 Init，让 applyDebug 在组件齐备的情况下重新 apply（幂等）。
	cfg.Init()

	return cfg
}

// OnReload 声明服务器配置不支持热重载。
//
// Address 与 listener 同理：进程启动时就已经绑好端口，改配置文件不会让它换地址。
// RegisterTTL / RegisterInterval 由后台注册协程按自己的节奏读取，热更它们等于在
// 一个跑着的 ticker 底下换参数，而收益只是「改注册间隔不用重启」。
//
// 被忽略的变更由框架逐条打印并提示需重启（见 config/reload.go）。
func (self *Config) OnReload(next any) error {
	return config.ErrReloadUnsupported
}

func (self *Config) Init(opts ...Option) {
	for _, opt := range opts {
		opt(self)
	}

	self.applyDebug()
}

// applyDebug 把 Debug 开关下发给各子组件。
//
// 每个组件都必须判空：newConfig 的调用顺序是 cfg.Init(opts...) 先跑、Transport /
// Router / Registry 后建，因此 Debug() option 生效时它们全是 nil。此前这里裸调
// self.Transport.Init(...) —— server.New(server.Debug()) 会直接 SIGSEGV。
// 组件建齐后 newConfig 会再调一次 Init()，届时本函数把开关补上；重复调用幂等。
func (self *Config) applyDebug() {
	if !self.Debug {
		return
	}

	if self.Transport != nil {
		self.Transport.Init(transport.Debug())
	}
	if self.Router != nil {
		self.Router.Config().Init(vrouter.Debug())
		self.Router.Config().RequestPrinter = true
		self.Router.Config().RouterTreePrinter = true
	}
	if self.Registry != nil {
		self.Registry.Config().Init(registry.Debug())
	}
}

// under debug mode the port will keep at 35999(http)/45999(rpc) by default.
//
// 只登记标志与端口，不碰子组件 —— 本 option 可能在 Transport / Router 建立之前
// 被 apply（见 newConfig）。真正的下发在 Config.applyDebug。
//
// 端口可用环境变量 VOLTS_DEBUG_HTTP_PORT / VOLTS_DEBUG_RPC_PORT 覆盖——本仓
// 原先所有走 cmd.Execute() 的进程（kylin 集成二进制、各独立微服务的 cmd）都
// 无条件调 volts.Debug()，端口写死意味着同一台机器上两个这样的进程不可能
// 同时跑（"bind: address already in use"，2026-07-24 独立 sale 进程真栈实测
// 撞出）。未设置环境变量时行为与之前完全一致（仍是 :35999/:45999），kylin
// 等既有部署零改动。
func Debug() Option {
	return func(cfg *Config) {
		cfg.Debug = true
		// Transport 为 nil 时走默认分支：newConfig 随后装配的是
		// transport.Default()，即 HttpTransport，故按 HTTP 端口取值。
		if _, ok := cfg.Transport.(*transport.HttpTransport); ok || cfg.Transport == nil {
			cfg.Address = ":" + envPortOr("VOLTS_DEBUG_HTTP_PORT", "35999")
		} else {
			cfg.Address = ":" + envPortOr("VOLTS_DEBUG_RPC_PORT", "45999")
		}
	}
}

func envPortOr(envKey, fallback string) string {
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	return fallback
}

func Logger() logger.ILogger {
	return log
}

// Name 设定服务器名，也是配置路径 server.<name>。只改字段：绑定在 New 里做一次。
func Name(name string) Option {
	return func(cfg *Config) {
		cfg.Name = name
	}
}

func WithBroker(bk broker.IBroker) Option {
	return func(cfg *Config) {
		cfg.Broker = bk
		cfg.BrokerType = bk.Config().Name
		cfg.BrokerHost = bk.Address() // FIXME
	}
}

// Registry used for discovery
func WithRegistry(r registry.IRegistry) Option {
	return func(cfg *Config) {
		if _, ok := r.(registry.IRegistryCacher); !ok {
			r = registry.NewCacher(r)
		}
		cfg.Registry = r
		cfg.RegistryType = r.Config().Name
		cfg.RegistryHost = r.Config().Addrs

		// Update Broker
		if cfg.Broker != nil {
			cfg.Broker.Init(broker.WithRegistry(r))
		}

		if cfg.Router != nil {
			cfg.Router.Config().Init(vrouter.WithRegistry(r))
		}
	}
}

// not accept other router
func WithRouter(router vrouter.IRouter) Option {
	return func(cfg *Config) {
		if _, ok := router.(*vrouter.TRouter); ok {
			cfg.Router = router

			// 避免执行顺序在WithRegistry后面
			if cfg.Registry != nil {
				cfg.Router.Config().Init(vrouter.WithRegistry(cfg.Registry))
			}
		}
	}
}

// Transport mechanism for communication e.g http, rabbitmq, etc
// WithTransport 注入传输层实例。它的配置结构体成为本 server 的 TransportConfig，
// 配置节从 transport.default 收编到 server.<name>.transport（见 config.Bind）。
func WithTransport(t transport.ITransport) Option {
	return func(cfg *Config) {
		cfg.Transport = t
	}
}

// Address to bind to - host:port or :port
func Address(addr string) Option {
	return func(cfg *Config) {
		cfg.Address = addr
		if cfg.Transport != nil {
			cfg.Transport.Config().Init(transport.WithAddrs(addr))
		}
	}
}

// Context specifies a context for the service.
// Can be used to signal shutdown of the service
// Can be used for extra option values.
func Context(ctx context.Context) Option {
	return func(cfg *Config) {
		cfg.Context = ctx
	}
}

// RegisterCheck run func before registry service
func RegisterCheck(fn func(context.Context) error) Option {
	return func(cfg *Config) {
		cfg.RegisterCheck = fn
	}
}

// Register the service with a TTL
func RegisterTTL(t time.Duration) Option {
	return func(cfg *Config) {
		cfg.RegisterTTL = t
	}
}

// Register the service with at interval
func RegisterInterval(t time.Duration) Option {
	return func(cfg *Config) {
		cfg.RegisterInterval = t
	}
}

// TLSConfig specifies a *tls.Config
func TLSConfig(t *tls.Config) Option {
	return func(cfg *Config) {
		// set the internal tls
		cfg.TLSConfig = t

		// set the default transport if one is not
		// already set. Required for Init call below.
		if cfg.Transport == nil {
			cfg.Transport = transport.NewHTTPTransport()
		}

		// set the transport tls
		cfg.Transport.Init(
			transport.Secure(true),
			transport.TLSConfig(t),
		)
	}
}

// 如果为False服务器不会自动创建默认必要组件实例,开发者自己配置
