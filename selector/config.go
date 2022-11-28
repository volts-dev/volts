package selector

import (
	"context"

	"github.com/volts-dev/volts/config"
	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/registry/noop"
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
		Logger         logger.ILogger `field:"-"` // 实例

		// Other options for implementations of the interface
		// can be stored in a context
		Context  context.Context    `field:"-"`
		Registry registry.IRegistry `field:"-"`
		Strategy Strategy           `field:"-"`
	}
)

func newConfig(opts ...Option) *Config {
	cfg := &Config{
		Logger:   log,
		Strategy: Random,
		Registry: noop.New(),
	}
	config.Default().Register(cfg)
	cfg.Init(opts...)
	return cfg
}

func (self *Config) String() string {
	return "selector"
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
