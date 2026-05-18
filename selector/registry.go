package selector

import (
	"sync"
	"time"

	"github.com/volts-dev/volts/registry"
)

type (
	registrySelector struct {
		config *Config
		// mu 保护 rc：Init 会 Stop 旧 cacher 然后替换为 newCache()。
		// 并发的 Match / Select / Close 必须看到一致的 rc，否则会调到已 Stop 的旧实例或 nil。
		mu sync.RWMutex
		rc registry.IRegistryCacher
	}
)

func (self *registrySelector) newCache() registry.IRegistryCacher {
	opts := []registry.Option{registry.WithConfigPrefixName(self.config.String())}
	if self.config.Context != nil {
		if t, ok := self.config.Context.Value("selector_ttl").(time.Duration); ok {
			opts = append(opts, registry.RegisterTTL(t))
		}
	}
	return registry.NewCacher(self.config.Registry, opts...)
}

func (self *registrySelector) Init(opts ...Option) error {
	for _, o := range opts {
		o(self.config)
	}

	self.mu.Lock()
	old := self.rc
	self.rc = self.newCache()
	self.mu.Unlock()
	// 在锁外 Stop 旧实例，避免 Stop().WaitGroup 与持有 mu 的并发 Match 形成依赖环
	if old != nil {
		old.Stop()
	}
	return nil
}

func (self *registrySelector) Config() *Config {
	return self.config
}

func (self *registrySelector) Match(endpoint string, opts ...SelectOption) (Next, error) {
	sopts := &SelectConfig{
		Strategy: self.config.Strategy,
	}
	self.mu.RLock()
	rc := self.rc
	self.mu.RUnlock()
	services, err := rc.Match(endpoint)
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
	self.mu.RLock()
	rc := self.rc
	self.mu.RUnlock()
	services, err := rc.GetService(service)
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
	self.mu.Lock()
	rc := self.rc
	self.rc = nil
	self.mu.Unlock()
	if rc != nil {
		rc.Stop()
	}
	return nil
}

func (c *registrySelector) String() string {
	return "registry"
}
