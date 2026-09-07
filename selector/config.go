package selector

import (
	"context"
	"time"

	"github.com/volts-dev/volts/config"
	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/registry"
)

type (
	// SelectOption used when making a select call
	SelectOption func(*SelectConfig)
	SelectConfig struct {
		Filters  []Filter
		Strategy Strategy

		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context
	}

	// OptionFn configures options of server.
	Option func(*Config)

	// Config 是选择器的配置。纯结构体：导出字段就是配置项，接口 / 函数类型的字段是
	// 实例引用，天然不进配置。
	//
	// 它通常被 router / client **拥有**（配置节 router.default.selector.*），独立创建时
	// 绑在 selector.<name>。
	Config struct {
		Name  string `field:"-"`
		Debug bool   `field:"-"`

		// BreakerFailures 触发节点熔断所需的连续失败次数。设为 0 关闭熔断。
		BreakerFailures int `field:"breaker_failures"`
		// BreakerCooldown 首次熔断的冷却时长，冷却期内该节点不参与选取。
		BreakerCooldown time.Duration `field:"breaker_cooldown"`
		// BreakerMaxCooldown 连续熔断指数退避后的冷却时长上限。
		BreakerMaxCooldown time.Duration `field:"breaker_max_cooldown"`

		// ---- 实例，不进配置文件 ----

		Logger   logger.ILogger // 保留:提供给扩展使用
		Registry registry.IRegistry
		Strategy Strategy
		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context

		use *Config // WithConfig
	}
)

// breakerSettings 一次性读出三个熔断参数。
//
// 三个必须**一起**读：它们逻辑上是一组设置，分三次读会横跨一次热重载，拿到新旧
// 混合的组合（新的阈值配旧的冷却时长）。读锁的理由见 config.HotRLock —— 这三个
// 字段都会被热重载改写，而每次 Select / Mark 都读它们。
func (self *Config) breakerSettings() (failures int, cooldown, maxCooldown time.Duration) {
	config.HotRLock()
	defer config.HotRUnlock()
	return self.BreakerFailures, self.BreakerCooldown, self.BreakerMaxCooldown
}

// BreakerEnabled 熔断是否开启（BreakerFailures > 0）。见 breakerSettings。
func (self *Config) BreakerEnabled() bool {
	config.HotRLock()
	defer config.HotRUnlock()
	return self.BreakerFailures > 0
}

// NewConfig 返回一份带代码默认值、**尚未绑定**到配置树的选择器配置，给拥有选择器的
// 组件作为字段带着、随自己一起 Bind，再用 New(WithConfig(cfg), ...) 建实例。
func NewConfig() *Config {
	return &Config{
		Name:               "default",
		Logger:             log,
		Strategy:           Random,
		Registry:           registry.NewNopRegistry(),
		BreakerFailures:    DefaultBreakerFailures,
		BreakerCooldown:    DefaultBreakerCooldown,
		BreakerMaxCooldown: DefaultBreakerMaxCooldown,
	}
}

func newConfig(opts ...Option) *Config {
	cfg := NewConfig()
	cfg.Init(opts...)
	if ext := cfg.use; ext != nil {
		cfg.use = nil
		// 调用方持有的配置可能是零值起家的（父结构体里 selector.NewConfig() 之外的写法），
		// 实例字段缺什么补什么。
		if ext.Logger == nil {
			ext.Logger = log
		}
		if ext.Strategy == nil {
			ext.Strategy = Random
		}
		if ext.Registry == nil {
			ext.Registry = registry.NewNopRegistry()
		}
		ext.Init(opts...)
		ext.use = nil
		return ext
	}
	config.Bind("selector."+cfg.Name, cfg)
	return cfg
}

func (self *Config) Init(opts ...Option) {
	for _, opt := range opts {
		if opt != nil {
			opt(self)
		}
	}
}

// WithConfig 让选择器实例使用调用方持有的这份配置（父组件的字段），不再自建、不再自绑。
func WithConfig(c *Config) Option {
	return func(cfg *Config) {
		cfg.use = c
	}
}

// WithName 设定独立绑定时的配置路径 selector.<name>。
func WithName(name string) Option {
	return func(cfg *Config) {
		cfg.Name = name
	}
}

func Debug() Option {
	return func(cfg *Config) {
		cfg.Debug = true
	}
}

func Logger() logger.ILogger {
	return log
}

// Registry sets the registry used by the selector
func Registry(r registry.IRegistry) Option {
	return func(cfg *Config) {
		cfg.Registry = r
	}
}

// WithFilter adds a filter function to the list of filters
// used during the Select call.
func WithFilter(fn ...Filter) SelectOption {
	return func(cfg *SelectConfig) {
		cfg.Filters = append(cfg.Filters, fn...)
	}
}

// WithBreaker 设置节点熔断参数。
// failures 为连续失败阈值，cooldown 为首次熔断的冷却时长（后续按次指数退避）。
func WithBreaker(failures int, cooldown time.Duration) Option {
	return func(cfg *Config) {
		cfg.BreakerFailures = failures
		cfg.BreakerCooldown = cooldown
	}
}

// WithoutBreaker 关闭节点熔断，失败节点将不再被摘除。
// 仅在外部已有熔断设施（如 service mesh sidecar）时才应关闭。
func WithoutBreaker() Option {
	return func(cfg *Config) {
		cfg.BreakerFailures = 0
	}
}

func WithStrategy(name string) Option {
	return func(cfg *Config) {
		if fn := Use(name); fn != nil {
			cfg.Strategy = Use(name)
		}
	}
}
