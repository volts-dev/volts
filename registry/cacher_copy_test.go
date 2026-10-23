package registry

import (
	"sync"
	"testing"
	"time"
)

// ownedRegistry 把自己内部的记录原样交出去 —— 真后端（etcd / memory / consul / mdns）
// 每次都新建对象，这个替身故意不守规矩，用来证明缓存器不依赖后端守规矩。
type ownedRegistry struct {
	IRegistry
	mu    sync.Mutex
	calls int
	fail  bool
	svcs  map[string][]*Service
}

func (r *ownedRegistry) GetService(name string) ([]*Service, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.fail {
		return nil, ErrNotFound
	}
	return r.svcs[name], nil
}
func (r *ownedRegistry) Watcher(...WatchOptions) (Watcher, error) { return newFakeWatcher(), nil }
func (r *ownedRegistry) String() string                           { return "owned-registry" }

func newOwnedCacher(t *testing.T, ttl time.Duration) (*registryCacher, *ownedRegistry) {
	t.Helper()
	reg := &ownedRegistry{svcs: map[string][]*Service{
		"sale": {{
			Name: "sale", Version: "latest",
			Nodes:     []*Node{{Id: "sale-1", Address: "10.0.0.1:1"}},
			Endpoints: []*Endpoint{{Name: "/sale.read", Path: "/sale.read"}},
		}},
	}}
	c, ok := NewCacher(reg, RegisterTTL(ttl)).(*registryCacher)
	if !ok {
		t.Fatal("NewCacher 没有返回 *registryCacher")
	}
	t.Cleanup(c.Stop)
	return c, reg
}

// 缓存器回源后只拷一份（此前进门一份、入缓存一份），缓存改成直接收下后端的对象。
// 这条锁住随之而来的义务：交出去的永远是副本 —— 调用方改它，既不能改到缓存，
// 也不能改到后端，两次调用拿到的也不能是同一个切片。
func TestCacher_GetServiceHandsOutIsolatedCopies(t *testing.T) {
	c, reg := newOwnedCacher(t, time.Minute)

	first, err := c.GetService("sale") // 回源
	if err != nil {
		t.Fatal(err)
	}
	first[0].Nodes[0].Address = "mutated"
	first[0].Nodes = nil
	first[0] = &Service{Name: "replaced"}

	second, err := c.GetService("sale") // 命中缓存
	if err != nil {
		t.Fatal(err)
	}
	reg.mu.Lock()
	calls := reg.calls
	reg.mu.Unlock()
	if calls != 1 {
		t.Fatalf("第二次应命中缓存，后端却被调了 %d 次", calls)
	}
	if second[0].Name != "sale" || len(second[0].Nodes) != 1 || second[0].Nodes[0].Address != "10.0.0.1:1" {
		t.Fatalf("调用方改回源结果改到了缓存: %+v", second[0])
	}
	if reg.svcs["sale"][0].Name != "sale" || len(reg.svcs["sale"][0].Nodes) != 1 {
		t.Fatalf("调用方改回源结果改到了后端: %+v", reg.svcs["sale"][0])
	}
	if &first[0] == &second[0] {
		t.Fatal("两次调用拿到的是同一个切片")
	}
}

// 回源失败退回旧缓存时同样交副本。
func TestCacher_GetServiceStaleFallbackIsACopy(t *testing.T) {
	c, reg := newOwnedCacher(t, time.Nanosecond) // 立刻过期，每次都回源

	if _, err := c.GetService("sale"); err != nil {
		t.Fatal(err)
	}
	reg.mu.Lock()
	reg.fail = true
	reg.mu.Unlock()

	stale, err := c.GetService("sale")
	if err != nil || len(stale) != 1 {
		t.Fatalf("回源失败应退回旧缓存，得到 %v, %v", stale, err)
	}
	stale[0].Nodes = nil

	c.RLock()
	cached := c.cache["sale"]
	c.RUnlock()
	if len(cached[0].Nodes) != 1 {
		t.Fatal("退回的旧缓存不是副本，调用方改到了缓存")
	}
}

// update() 会**原地**改缓存（services[i] = …、service.Nodes = …），所以读侧的复制
// 必须在锁里。把 Copy 挪到 RUnlock 之后这条就会在 -race 下红。
func TestCacher_CopyDoesNotRaceWatchUpdate(t *testing.T) {
	c, _ := newOwnedCacher(t, time.Hour)
	if _, err := c.GetService("sale"); err != nil { // 播种缓存并登记 watched
		t.Fatal(err)
	}

	node := func(id string) *Service {
		return &Service{
			Name: "sale", Version: "latest",
			Nodes:     []*Node{{Id: id, Address: id + ":1"}},
			Endpoints: []*Endpoint{{Name: "/sale.read", Path: "/sale.read"}},
		}
	}

	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				if _, err := c.GetService("sale"); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	for i := 0; i < 300; i++ {
		c.update(&Result{Action: "update", Service: node("sale-2")})
		c.update(&Result{Action: "delete", Service: node("sale-2")})
	}
	wg.Wait()
}
