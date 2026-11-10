package selector

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/volts-dev/volts/registry"
)

type cacheBackend struct {
	registry.IRegistry
	mu       sync.Mutex
	node     string
	watchers []*cacheWatcher
	starts   atomic.Int64
	stops    atomic.Int64
}
type cacheWatcher struct {
	backend *cacheBackend
	events  chan *registry.Result
	exit    chan struct{}
	once    sync.Once
}

func (r *cacheBackend) GetService(name string) ([]*registry.Service, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return []*registry.Service{{Name: name, Nodes: []*registry.Node{{Id: r.node}}}}, nil
}
func (r *cacheBackend) Watcher(...registry.WatchOptions) (registry.Watcher, error) {
	w := &cacheWatcher{backend: r, events: make(chan *registry.Result, 8), exit: make(chan struct{})}
	r.mu.Lock()
	r.watchers = append(r.watchers, w)
	r.mu.Unlock()
	r.starts.Add(1)
	return w, nil
}
func (w *cacheWatcher) Next() (*registry.Result, error) {
	select {
	case res := <-w.events:
		return res, nil
	case <-w.exit:
		return nil, registry.ErrWatcherStopped
	}
}
func (w *cacheWatcher) Stop() { w.once.Do(func() { w.backend.stops.Add(1); close(w.exit) }) }
func newCacheSelector(r registry.IRegistry, ttl ...time.Duration) *registrySelector {
	cfg := NewConfig()
	cfg.Registry = r
	if len(ttl) > 0 {
		cfg.Context = context.WithValue(context.Background(), "selector_ttl", ttl[0])
	}
	return New(WithConfig(cfg)).(*registrySelector)
}
func cacheEventually(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("cache lifecycle did not converge")
}
func cacheSelect(t *testing.T, s ISelector) string {
	t.Helper()
	next, err := s.Select("sale")
	if err != nil {
		t.Fatal(err)
	}
	n, err := next()
	if err != nil {
		t.Fatal(err)
	}
	return n.Id
}
func TestSelectorCache_SharesOnlyBackendIdentityAndTTL(t *testing.T) {
	a, b := &cacheBackend{node: "a"}, &cacheBackend{node: "b"}
	first, second := newCacheSelector(a), newCacheSelector(a)
	other, ttl := newCacheSelector(b), newCacheSelector(a, time.Second)
	for _, s := range []*registrySelector{first, second, other, ttl} {
		defer s.Close()
	}
	if first.rc != second.rc {
		t.Fatal("compatible selectors do not share cache")
	}
	if first.rc == other.rc || first.rc == ttl.rc {
		t.Fatal("different backend identity/TTL shared cache")
	}
	if cacheSelect(t, first) != "a" || cacheSelect(t, other) != "b" {
		t.Fatal("backend isolation lost")
	}
	// 健康状态不随发现缓存共享。
	first.health.mark("sale", "a", fmt.Errorf("failure"), first.config)
	size := 0
	second.health.states.Range(func(_, _ any) bool { size++; return true })
	if size != 0 {
		t.Fatal("breaker state shared between selectors")
	}
}
func TestSelectorCache_InitAndCloseRespectOtherOwners(t *testing.T) {
	r := &cacheBackend{node: "a"}
	a, b := newCacheSelector(r), newCacheSelector(r)
	defer a.Close()
	defer b.Close()
	cacheSelect(t, a)
	cacheSelect(t, b)
	cacheEventually(t, func() bool { return r.starts.Load() == 1 })
	original := a.rc
	for i := 0; i < 100; i++ {
		if err := a.Init(Registry(r)); err != nil {
			t.Fatal(err)
		}
	}
	if a.rc != original {
		t.Fatal("same registry Init replaced hot cache")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if r.stops.Load() != 0 || cacheSelect(t, b) != "a" {
		t.Fatal("one owner stopped its sibling")
	}
	if _, err := a.Select("sale"); err != registry.ErrWatcherStopped {
		t.Fatalf("closed select: %v", err)
	}
	if _, err := a.Match("/sale"); err != registry.ErrWatcherStopped {
		t.Fatalf("closed match: %v", err)
	}
	if err := a.Init(Registry(r)); err != registry.ErrWatcherStopped {
		t.Fatal("Init revived closed selector")
	}
	b.Close()
	if r.stops.Load() != 1 {
		t.Fatalf("last close stopped %d watchers", r.stops.Load())
	}
	selectorCaches.Lock()
	_, exists := selectorCaches.entries[selectorCacheKey{backend: r, ttl: registry.DefaultTTL}]
	selectorCaches.Unlock()
	if exists {
		t.Fatal("last close retained backend/cache")
	}
}
func TestSelectorCache_InjectedCacherIsBorrowed(t *testing.T) {
	r := &cacheBackend{node: "a"}
	owned := registry.NewCacher(r)
	defer owned.Stop()
	a := newCacheSelector(owned)
	if a.rc != owned {
		t.Fatal("injected cacher wrapped again")
	}
	cacheSelect(t, a)
	cacheEventually(t, func() bool { return r.starts.Load() == 1 })
	a.Close()
	if r.stops.Load() != 0 {
		t.Fatal("borrower stopped owner cache")
	}
	if _, err := owned.GetService("sale"); err != nil {
		t.Fatal(err)
	}
	owned.Stop()
	if r.stops.Load() != 1 {
		t.Fatal("owner did not stop watcher")
	}
}
func TestSelectorCache_InjectedDifferentTTLIsNotBorrowed(t *testing.T) {
	r := &cacheBackend{node: "a"}
	owned := registry.NewCacher(r, registry.RegisterTTL(time.Hour))
	defer owned.Stop()
	a := newCacheSelector(owned)
	defer a.Close()
	if a.rc == owned {
		t.Fatal("different TTL silently borrowed")
	}
	cacheSelect(t, a)
	cacheEventually(t, func() bool { return r.starts.Load() == 2 })
	a.Close()
	if r.stops.Load() != 1 {
		t.Fatal("closing child must stop only its own watcher")
	}
	if _, err := owned.GetService("sale"); err != nil {
		t.Fatal(err)
	}
	owned.Stop()
	if r.stops.Load() != 2 {
		t.Fatal("owner watcher did not stop")
	}
}

func TestSelectorCache_WatchUpdatesReachAllReaders(t *testing.T) {
	r := &cacheBackend{node: "a"}
	a, b := newCacheSelector(r), newCacheSelector(r)
	defer a.Close()
	defer b.Close()
	cacheSelect(t, a)
	cacheSelect(t, b)
	cacheEventually(t, func() bool { return r.starts.Load() == 1 })
	r.mu.Lock()
	w := r.watchers[0]
	r.node = "b"
	r.mu.Unlock()
	w.events <- &registry.Result{Action: "delete", Service: &registry.Service{Name: "sale"}}
	cacheEventually(t, func() bool { return cacheSelect(t, a) == "b" && cacheSelect(t, b) == "b" })
}
func TestSelectorCache_ConcurrentLifecycleConverges(t *testing.T) {
	r := &cacheBackend{node: "a"}
	anchor := newCacheSelector(r)
	defer anchor.Close()
	cacheSelect(t, anchor)
	cacheEventually(t, func() bool { return r.starts.Load() == 1 })
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				s := newCacheSelector(r)
				_, _ = s.Select("sale")
				_ = s.Init(Registry(r))
				s.Close()
				s.Close()
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent lifecycle hung")
	}
	if r.starts.Load() != 1 || r.stops.Load() != 0 {
		t.Fatal("concurrent borrowers churned watcher")
	}
	anchor.Close()
	if r.stops.Load() != 1 {
		t.Fatal("last owner did not drain watcher")
	}
}

func BenchmarkSelectorCacheFanout(b *testing.B) {
	for i := 0; i < b.N; i++ {
		r := &cacheBackend{node: "a"}
		selectors := make([]*registrySelector, 200)
		for j := range selectors {
			selectors[j] = newCacheSelector(r)
			_, _ = selectors[j].Select("sale")
		}
		deadline := time.Now().Add(2 * time.Second)
		for r.starts.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(150 * time.Millisecond)
		b.ReportMetric(float64(r.starts.Load()), "watchers/200-selectors")
		for _, s := range selectors {
			s.Close()
		}
	}
}
