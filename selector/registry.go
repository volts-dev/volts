package selector

import (
	"sync"

	"github.com/volts-dev/volts/registry"
)

type (
	registrySelector struct {
		config    *Config
		lifecycle sync.Mutex
		// mu 保护 rc 与租约交换；lifecycle 串行化 Init/Close，等待清理不占读锁。
		mu           sync.RWMutex
		rc           registry.IRegistryCacher
		releaseCache func()
		// health 追踪各节点的失败情况，用于摘除/熔断（见 breaker.go）。
		health healthTracker
	}
)

func (self *registrySelector) Init(opts ...Option) error {
	self.lifecycle.Lock()
	defer self.lifecycle.Unlock()
	self.mu.Lock()
	if self.rc == nil {
		self.mu.Unlock()
		return registry.ErrWatcherStopped
	}
	for _, o := range opts {
		if o != nil {
			o(self.config)
		}
	}
	// 先获取新租约再放旧租约。同配置 Init 不重建 watcher，也不冲掉热缓存。
	rc, release := acquireSelectorCache(self.config)
	oldRelease := self.releaseCache
	self.rc, self.releaseCache = rc, release
	self.mu.Unlock()
	oldRelease()
	return nil
}

func (self *registrySelector) Config() *Config {
	return self.config
}

func (self *registrySelector) Match(endpoint string, opts ...SelectOption) (Next, error) {
	self.mu.RLock()
	sopts := &SelectConfig{Strategy: self.config.Strategy}
	rc := self.rc
	self.mu.RUnlock()
	if rc == nil {
		return nil, registry.ErrWatcherStopped
	}
	services, err := rc.Match(endpoint)
	if err != nil {
		if err == registry.ErrNotFound {
			return nil, ErrNotFound
		}
		return nil, err
	}
	// 摘掉处于熔断状态的节点（与 Select 同规则）
	if self.config.BreakerEnabled() {
		self.health.purge()
		services = self.health.filterHealthy(services)
	}

	// if there's nothing left, return
	if len(services) == 0 {
		return nil, ErrNoneAvailable
	}

	return sopts.Strategy(services), nil
}

func (self *registrySelector) Select(service string, opts ...SelectOption) (Next, error) {
	self.mu.RLock()
	sopts := &SelectConfig{Strategy: self.config.Strategy}
	rc := self.rc
	self.mu.RUnlock()

	for _, opt := range opts {
		opt(sopts)
	}

	// get the service
	// try the cache first
	// if that fails go directly to the registry
	if rc == nil {
		return nil, registry.ErrWatcherStopped
	}
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

	// 摘掉处于熔断状态的节点。放在用户 filter 之后，确保用户的筛选逻辑
	// 看到的是完整节点集（否则 filter 的语义会随节点健康度漂移）。
	if self.config.BreakerEnabled() {
		self.health.purge()
		services = self.health.filterHealthy(services)
	}

	// if there's nothing left, return
	if len(services) == 0 {
		return nil, ErrNoneAvailable
	}

	return sopts.Strategy(services), nil
}

// Mark 记录一次针对该节点的调用结果，err 为 nil 表示成功。
//
// 连续失败达到 Config.BreakerFailures 次即熔断该节点，冷却期内不再被 Select
// 选中；成功一次即完全恢复。详见 breaker.go。
//
// 此前本方法是空函数 —— 挂掉的节点会被持续选中，直到注册中心 TTL 过期
// （分钟级），期间每个落到它头上的请求都必然失败。
func (self *registrySelector) Mark(service string, node *registry.Node, err error) {
	if node == nil || !self.config.BreakerEnabled() {
		return
	}
	self.health.mark(service, node.Id, err, self.config)
}

// Reset 清空指定服务的全部节点健康度状态，立即解除其熔断。
// service 为空字符串时清空所有服务。
func (self *registrySelector) Reset(service string) {
	self.health.reset(service)
}

// Close stops the watcher and destroys the cache
func (self *registrySelector) Close() error {
	self.lifecycle.Lock()
	defer self.lifecycle.Unlock()
	self.mu.Lock()
	release := self.releaseCache
	self.rc, self.releaseCache = nil, nil
	self.mu.Unlock()
	if release != nil {
		release()
	}
	return nil
}

func (c *registrySelector) String() string {
	return "registry"
}
