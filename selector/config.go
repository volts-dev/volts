package selector

import (
	"context"
	"strings"
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
	Config struct {
		config.Config `field:"-"`
		Name          string             `field:"-"` // config name/path in config file
		PrefixName    string             `field:"-"` // config prefix name
		Logger        logger.ILogger     `field:"-"` // 保留:提供给扩展使用
		Registry      registry.IRegistry `field:"-"`
		Strategy      Strategy           `field:"-"`

		// BreakerFailures 触发节点熔断所需的连续失败次数。设为 0 关闭熔断。
		BreakerFailures int `field:"breaker_failures"`
		// BreakerCooldown 首次熔断的冷却时长，冷却期内该节点不参与选取。
		BreakerCooldown time.Duration `field:"breaker_cooldown"`
		// BreakerMaxCooldown 连续熔断指数退避后的冷却时长上限。
		BreakerMaxCooldown time.Duration `field:"breaker_max_cooldown"`

		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context `field:"-"`
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

func newConfig(opts ...Option) *Config {
	cfg := &Config{
		Name:               "default",
		Logger:             log,
		Strategy:           Random,
		Registry:           registry.NewNopRegistry(),
		BreakerFailures:    DefaultBreakerFailures,
		BreakerCooldown:    DefaultBreakerCooldown,
		BreakerMaxCooldown: DefaultBreakerMaxCooldown,
	}
	cfg.Init(opts...)
	config.Register(cfg)
	return cfg
}

func (self *Config) String() string {
	if len(self.PrefixName) > 0 {
		return strings.Join([]string{self.PrefixName, "selector"}, ".")
	}

	if len(self.Name) > 0 {
		return strings.Join([]string{"selector", self.Name}, ".")
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

// 修改Config.json的路径
func WithConfigPrefixName(prefixName string) Option {
	return func(cfg *Config) {
		cfg.Unregister(cfg)
		cfg.PrefixName = prefixName
		cfg.Register(cfg)
	}
}
