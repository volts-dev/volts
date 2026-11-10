package selector

import (
	"reflect"
	"sync"
	"time"

	"github.com/volts-dev/volts/registry"
)

// 按后端实例身份而不是地址共享：同地址的 TLS、凭据、命名空间可以不同。
// 不拆开包装层，不共享 selector 的熔断器、过滤器或请求身份。
type selectorCacheKey struct {
	backend registry.IRegistry
	ttl     time.Duration
}
type selectorCacheEntry struct {
	cache registry.IRegistryCacher
	refs  int
}

var selectorCaches = struct {
	sync.Mutex
	entries map[selectorCacheKey]*selectorCacheEntry
}{entries: make(map[selectorCacheKey]*selectorCacheEntry)}

func acquireSelectorCache(cfg *Config) (registry.IRegistryCacher, func()) {
	ttl := registry.DefaultTTL
	if cfg.Context != nil {
		if t, ok := cfg.Context.Value("selector_ttl").(time.Duration); ok {
			ttl = t
		}
	}
	// 只借用 TTL 明确兼容的 cacher。Config() 是后端配置，不能拿它推断缓存 TTL。
	// 注入者仍拥有 Stop；借用者 Close 不能停掉 server/router 的发现。
	if c, ok := cfg.Registry.(registry.IRegistryCacher); ok {
		if policy, ok := c.(interface{ CacheTTL() time.Duration }); ok && policy.CacheTTL() == ttl {
			return c, func() {}
		}
	}
	// 只分享有明确指针身份的实例；自定义值后端保守地走独立生命周期。
	kind := reflect.ValueOf(cfg.Registry).Kind()
	if kind != reflect.Ptr {
		c := registry.NewCacher(cfg.Registry, registry.RegisterTTL(ttl))
		return c, sync.OnceFunc(c.Stop)
	}
	key := selectorCacheKey{backend: cfg.Registry, ttl: ttl}
	selectorCaches.Lock()
	entry := selectorCaches.entries[key]
	if entry == nil {
		entry = &selectorCacheEntry{cache: registry.NewCacher(cfg.Registry, registry.RegisterTTL(ttl))}
		selectorCaches.entries[key] = entry
	}
	entry.refs++
	selectorCaches.Unlock()
	return entry.cache, sync.OnceFunc(func() {
		selectorCaches.Lock()
		entry.refs--
		last := entry.refs == 0
		if last {
			delete(selectorCaches.entries, key)
		}
		selectorCaches.Unlock()
		// 不能持全局锁等后端 watcher 退出，别的配置的生命周期应保持独立。
		if last {
			entry.cache.Stop()
		}
	})
}
