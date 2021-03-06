package selector

import (
	"errors"

	"github.com/volts-dev/volts/registry"
)

var (
	DefaultSelector = NewSelector()

	ErrNotFound      = errors.New("not found")
	ErrNoneAvailable = errors.New("none available")
)

type (
	// Selector builds on the registry as a mechanism to pick nodes
	// and mark their status. This allows host pools and other things
	// to be built using various algorithms.
	ISelector interface {
		Init(opts ...Option) error
		Config() *Config
		// Select returns a function which should return the next node
		Select(service string, opts ...SelectOption) (Next, error)
		// Mark sets the success/error against a node
		Mark(service string, node *registry.Node, err error)
		// Reset returns state back to zero for a service
		Reset(service string)
		// Close renders the selector unusable
		Close() error
		// Name of the selector
		String() string
	}

	// Next is a function that returns the next node
	// based on the selector's strategy
	Next func() (*registry.Node, error)

	// Filter is used to filter a service during the selection process
	Filter func([]*registry.Service) []*registry.Service

	// Strategy is a selection strategy e.g random, round robin
	Strategy func([]*registry.Service) Next
)

func NewSelector(opts ...Option) ISelector {
	cfg := &Config{
		Strategy: Random,
	}

	for _, opt := range opts {
		opt(cfg)
	}

	if cfg.Registry == nil {
		cfg.Registry = registry.DefaultRegistry
	}

	s := &registrySelector{
		config: cfg,
	}
	s.rc = s.newCache()

	return s
}
