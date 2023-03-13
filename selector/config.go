package selector

import (
	"context"
	"strings"

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
		*config.Config `field:"-"`
		Name           string         `field:"-"` // config name/path in config file
		PrefixName     string         `field:"-"` // config prefix name
		Logger         logger.ILogger `field:"-"` // 保留:提供给扩展使用

		// Other options for implementations of the interface
		// can be stored in a context
		Context  context.Context    `field:"-"`
		Registry registry.IRegistry `field:"-"`
		Strategy Strategy           `field:"-"`
	}
)

func newConfig(opts ...Option) *Config {
	cfg := &Config{
		Name:     "selector",
		Logger:   log,
		Strategy: Random,
		Registry: registry.Default(),
	}
	cfg.Init(opts...)
	config.Register(cfg)
	return cfg
}

func (self *Config) String() string {
	if len(self.PrefixName) > 0 {
		return strings.Join([]string{self.PrefixName, self.Name}, ".")
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

func (self *Config) Load() error {
	return self.LoadToModel(self)
}

func (self *Config) Save(immed ...bool) error {
	return self.SaveFromModel(self, immed...)
}

func Debug() Option {
	return func(cfg *Config) {
		cfg.Debug = true
	}
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
		cfg.PrefixName = prefixName
		// 重新加载
		cfg.Load()
	}
}
