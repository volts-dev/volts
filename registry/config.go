package registry

import (
	"context"
	"crypto/tls"
	"time"

	"github.com/volts-dev/volts/config"
	"github.com/volts-dev/volts/logger"
)

type servicesKey struct{}

type (
	Option       func(*Config)
	WatchOptions func(*WatchConfig) error

	RegisterConfig struct {
		TTL time.Duration
		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context
	}

	DeregisterConfig struct {
		Context context.Context
	}

	GetConfig struct {
		Context context.Context
	}

	ListConfig struct {
		Context context.Context
	}

	// Config 是注册中心的配置。纯结构体：导出字段就是配置项，接口类型的字段是实例引用，
	// 天然不进配置。
	//
	// registry 是跨组件**共享**的实例（server / router / client 按类型与地址引用它），
	// 所以它的配置是根节 registry.<name>，不挂在任何持有者下面；缓存层（registryCacher）
	// 用的是一份不绑定的配置，见 newCacheConfig。
	Config struct {
		Name      string      `field:"-"` // 配置路径 registry.<name>；也是 Use 的实例缓存键之一
		Debug     bool        `field:"-"`
		Addrs     []string    `field:"-"` // 地址由持有者（server.RegistryHost）给出，不在本节
		TlsConfig *tls.Config `field:"-"`

		Timeout time.Duration
		Secure  bool
		TTL     time.Duration `field:"ttl"`

		// ---- 实例，不进配置文件 ----
		Logger  logger.ILogger
		Context context.Context
	}

	WatchConfig struct {
		// Specify a service to watch
		// If blank, the watch is for all services
		Service string
		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context
	}
)

// NewConfig 给各注册中心实现的构造函数用：默认值 → options → 绑到 registry.<name>。
func NewConfig(opts ...Option) *Config {
	cfg := newCacheConfig(opts...)
	config.Bind("registry."+cfg.Name, cfg)
	return cfg
}

// newCacheConfig 返回带默认值、**不绑定**的配置。
//
// registryCacher 用它：缓存层的 TTL 是实现参数，不是配置文件里的一节。此前每个持有
// registry 的组件（server / router / selector / client）都在自己的前缀下把 cacher 的
// 这份配置登记成 <prefix>.registry，一个 etcd 实例的配置于是在真栈文件里出现了 20 多份。
// registry.Use 解析 options 查实例缓存时也用它。
func newCacheConfig(opts ...Option) *Config {
	cfg := &Config{
		Name:    "default",
		Logger:  log,
		Context: context.Background(),
		// 5s 而不是原来的 100ms。这个默认值会被 config.Register 持久化进配置
		// 文件，而且每个内嵌 registry 的组件（server/router/client/selector）都
		// 会在自己的前缀下留一份副本 —— 100ms 因此扩散到几十处，并让 etcd 侧
		// 「Timeout==0 才兜底」的逻辑永不触发。亚秒级超时对网络型后端
		// （etcd/consul）没有合理用途：本机 etcd 拉一次全量服务列表实测就要
		// 77~130ms，踩线的结果是 context deadline exceeded 一路透传成 HTTP 500。
		Timeout: 5 * time.Second,
	}
	cfg.Init(opts...)
	return cfg
}

func (self *Config) Init(opts ...Option) {
	for _, opt := range opts {
		if opt != nil {
			opt(self)
		}
	}
}

func Logger() logger.ILogger {
	return log
}

func Debug() Option {
	return func(cfg *Config) {
		cfg.Debug = true
	}
}

// Addrs is the registry addresses to use
func Addrs(addrs ...string) Option {
	return func(cfg *Config) {
		cfg.Addrs = addrs
	}
}

func Timeout(t time.Duration) Option {
	return func(cfg *Config) {
		cfg.Timeout = t
	}
}

// Secure communication with the registry
func Secure(b bool) Option {
	return func(cfg *Config) {
		cfg.Secure = b
	}
}

// Specify TLS Config
func TLSConfig(t *tls.Config) Option {
	return func(cfg *Config) {
		cfg.TlsConfig = t
	}
}

func RegisterTTL(t time.Duration) Option {
	return func(cfg *Config) {
		cfg.TTL = t
	}
}

// change registry name
func WithName(name string) Option {
	return func(cfg *Config) {
		//cfg.Unregister(cfg)
		cfg.Name = name
		//cfg.Register(cfg)
	}
}
