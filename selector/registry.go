package selector

import (
	"time"

	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/registry/cacher"
)

type (
	registrySelector struct {
		config *Config
		rc     cacher.ICacher
	}
)

func (self *registrySelector) newCache() cacher.ICacher {
	opts := []registry.Option{}
	if self.config.Context != nil {
		if t, ok := self.config.Context.Value("selector_ttl").(time.Duration); ok {
			opts = append(opts, registry.RegisterTTL(t))
		}
	}
	return cacher.New(self.config.Registry, opts...)
}

func (self *registrySelector) Init(opts ...Option) error {
	for _, o := range opts {
		o(self.config)
	}

	self.rc.Stop()
	self.rc = self.newCache()

	return nil
}

func (self *registrySelector) Config() *Config {
	return self.config
}

func (self *registrySelector) Match(endpoint string, opts ...SelectOption) (Next, error) {
	sopts := &SelectConfig{
		Strategy: self.config.Strategy,
	}
	services, err := self.rc.Match(endpoint)
	if err != nil {
		if err == registry.ErrNotFound {
			return nil, ErrNotFound
		}
		return nil, err
	}
	// if there's nothing left, return
	if len(services) == 0 {
		return nil, ErrNoneAvailable
	}

	return sopts.Strategy(services), nil
}

func (self *registrySelector) Select(service string, opts ...SelectOption) (Next, error) {
	sopts := &SelectConfig{
		Strategy: self.config.Strategy,
	}

	for _, opt := range opts {
		opt(sopts)
	}

	// get the service
	// try the cache first
	// if that fails go directly to the registry
	services, err := self.rc.GetService(service)
	if err != nil {
		if err == registry.ErrNotFound {
			return nil, ErrNotFound
		}
		return nil, err
	}

	// apply the filters
	for _, filter := range sopts.Filters {
		services = filter(services)
	}

	// if there's nothing left, return
	if len(services) == 0 {
		return nil, ErrNoneAvailable
	}

	return sopts.Strategy(services), nil
}

func (self *registrySelector) Mark(service string, node *registry.Node, err error) {
}

func (self *registrySelector) Reset(service string) {
}

// Close stops the watcher and destroys the cache
func (self *registrySelector) Close() error {
	self.rc.Stop()

	return nil
}

func (c *registrySelector) String() string {
	return "registry"
}
