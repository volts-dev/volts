package selector

import (
	"context"

	"github.com/volts-dev/volts/registry"
)

type (
	// OptionFn configures options of server.
	Option func(*Config) error
	Config struct {
		Registry registry.IRegistry
		Strategy Strategy

		// Other options for implementations of the interface
		// can be stored in a context
		Context context.Context
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

// Registry sets the registry used by the selector
func WithRegistry(r registry.IRegistry) Option {
	return func(cfg *Config) error {
		cfg.Registry = r
		return nil
	}
}
