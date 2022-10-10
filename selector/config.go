package selector

import (
	"context"

	"github.com/volts-dev/volts/config"
	"github.com/volts-dev/volts/registry"
)

type (
	// OptionFn configures options of server.
	Option func(*Config) error
	Config struct {
		*config.Config `field:"-"`
		// Other options for implementations of the interface
		// can be stored in a context
		Context  context.Context    `field:"-"`
		Registry registry.IRegistry `field:"-"`
		Strategy Strategy           `field:"-"`
	}

	// SelectOption used when making a select call
	SelectOption func(*SelectConfig)
	SelectConfig struct {
		Filters  []Filter
		Strategy Strategy

		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context
	}
)

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

func (self *Config) Save() error {
	return self.Config.Save(config.WithConfig(self))
}

// Registry sets the registry used by the selector
func Registry(r registry.IRegistry) Option {
	return func(cfg *Config) error {
		cfg.Registry = r
		return nil
	}
}

// WithFilter adds a filter function to the list of filters
// used during the Select call.
func WithFilter(fn ...Filter) SelectOption {
	return func(cfg *SelectConfig) {
		cfg.Filters = append(cfg.Filters, fn...)
	}
}
