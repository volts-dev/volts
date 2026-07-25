package registry

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// countingRegistry 记录 ListServices 的真实回源次数，并可切换成返回错误。
type countingRegistry struct {
	IRegistry
	mu    sync.Mutex
	calls int
	fail  error
	svcs  []*Service
	w     *fakeWatcher
}

func (r *countingRegistry) Init(...Option) error                 { return nil }
func (r *countingRegistry) Config() *Config                      { return &Config{} }
func (r *countingRegistry) Register(*Service, ...Option) error   { return nil }
func (r *countingRegistry) Deregister(*Service, ...Option) error { return nil }
func (r *countingRegistry) GetService(name string) ([]*Service, error) {
	return []*Service{{Name: name}}, nil
}
func (r *countingRegistry) Watcher(...WatchOptions) (Watcher, error) { return r.w, nil }
func (r *countingRegistry) String() string                           { return "counting-registry" }

func (r *countingRegistry) ListServices() ([]*Service, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.fail != nil {
		return nil, r.fail
	}
	return r.svcs, nil
}

func (r *countingRegistry) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *countingRegistry) setFail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fail = err
}

func newCountingCacher(t *testing.T, ttl time.Duration) (*registryCacher, *countingRegistry) {
	t.Helper()
	reg := &countingRegistry{
		w: newFakeWatcher(),
		svcs: []*Service{
			{Name: "alpha", Endpoints: []*Endpoint{{Name: "/alpha.read", Path: "/alpha.read"}}},
			{Name: "beta", Endpoints: []*Endpoint{{Name: "/beta.read", Path: "/beta.read"}}},
		},
	}
	c, ok := NewCacher(reg, RegisterTTL(ttl)).(*registryCacher)
	if !ok {
		t.Fatal("NewCacher 没有返回 *registryCacher")
	}
	return c, reg
}

// ListServices 必须真的缓存。此前 registryCacher 只是嵌入 IRegistry、不覆写这个
// 方法，于是每次调用都穿透到后端——它又处在「按模型找属主服务」的必经路径上，
// 每个网关请求都要全量拉一遍注册数据。
func TestCacher_ListServicesIsCached(t *testing.T) {
	c, reg := newCountingCacher(t, time.Minute)
	defer c.Stop()

	for i := 0; i < 10; i++ {
		svcs, err := c.ListServices()
		if err != nil {
			t.Fatalf("第 %d 次 ListServices 失败: %v", i, err)
		}
		if len(svcs) != 2 {
			t.Fatalf("第 %d 次返回 %d 个服务，期望 2", i, len(svcs))
		}
	}

	if n := reg.count(); n != 1 {
		t.Errorf("10 次调用回源 %d 次，期望 1 次（其余走缓存）", n)
	}
}

// TTL 到期后必须重新回源，否则注册中心的变化永远看不到。
func TestCacher_ListServicesExpires(t *testing.T) {
	c, reg := newCountingCacher(t, 30*time.Millisecond)
	defer c.Stop()

	if _, err := c.ListServices(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	if _, err := c.ListServices(); err != nil {
		t.Fatal(err)
	}

	if n := reg.count(); n != 2 {
		t.Errorf("跨过 TTL 后回源 %d 次，期望 2 次", n)
	}
}

// 注册/注销事件要让清单缓存立即失效：新服务不该等满一个 TTL 才可见。
// 关键在于失效发生在「只保留已 watch 服务」那道过滤**之前**——新上线的服务
// 恰恰还没被 watch。
func TestCacher_WatchEventInvalidatesList(t *testing.T) {
	c, reg := newCountingCacher(t, time.Minute)
	defer c.Stop()

	if _, err := c.ListServices(); err != nil {
		t.Fatal(err)
	}
	if n := reg.count(); n != 1 {
		t.Fatalf("首次回源 %d 次，期望 1", n)
	}

	// 一个从未被 watch 过的新服务上线
	c.update(&Result{Action: "create", Service: &Service{Name: "gamma"}})

	if _, err := c.ListServices(); err != nil {
		t.Fatal(err)
	}
	if n := reg.count(); n != 2 {
		t.Errorf("watch 事件后回源 %d 次，期望 2（缓存应已失效）", n)
	}
}

// 回源失败时应退回陈旧缓存，而不是把错误抛给调用方：服务清单短暂过期，
// 远好过整条跨服务链路直接报错。
func TestCacher_ListServicesFallsBackToStale(t *testing.T) {
	c, reg := newCountingCacher(t, 20*time.Millisecond)
	defer c.Stop()

	if _, err := c.ListServices(); err != nil {
		t.Fatal(err)
	}

	reg.setFail(errors.New("registry unreachable"))
	time.Sleep(40 * time.Millisecond)

	svcs, err := c.ListServices()
	if err != nil {
		t.Fatalf("后端故障时应返回陈旧缓存，实得错误: %v", err)
	}
	if len(svcs) != 2 {
		t.Errorf("陈旧缓存返回 %d 个服务，期望 2", len(svcs))
	}
}

// 无缓存可退时才应把错误抛出去。
func TestCacher_ListServicesErrorsWithoutCache(t *testing.T) {
	c, reg := newCountingCacher(t, time.Minute)
	defer c.Stop()

	reg.setFail(errors.New("registry unreachable"))
	if _, err := c.ListServices(); err == nil {
		t.Error("首次调用即失败且无缓存可退，应返回错误")
	}
}

// match 出错时曾经 return 而不 RUnlock，读锁永久泄漏：此后任何写者
// （watch 事件）永远阻塞，Go 的 RWMutex 又让后续 RLock 全排在写者之后
// ——一次瞬时抖动就把整个缓存器永久卡死。
//
// 本测试先让一次 match 失败，再要求「写锁仍拿得到、后续读仍能返回」。
// 未修版本会在这里超时。
func TestCacher_MatchErrorDoesNotLeakLock(t *testing.T) {
	c, reg := newCountingCacher(t, time.Minute)
	defer c.Stop()

	reg.setFail(errors.New("registry unreachable"))
	if _, err := c.match("/nope.read"); err == nil {
		t.Fatal("后端故障时 match 应返回错误")
	}

	done := make(chan struct{})
	go func() {
		// update 取写锁；读锁若泄漏，这里永远拿不到
		c.update(&Result{Action: "create", Service: &Service{Name: "gamma"}})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("match 出错后写锁拿不到 —— 读锁泄漏了（缓存器已永久卡死）")
	}
}

// match 在读锁下写 endpointMap，并发调用会触发 "concurrent map writes" 直接
// 崩掉进程。-race 下跑并发 match 来守住这条。
func TestCacher_MatchConcurrentIsRaceFree(t *testing.T) {
	c, _ := newCountingCacher(t, time.Minute)
	defer c.Stop()

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				c.match("/alpha.read")
			} else {
				c.match("/beta.read")
			}
		}(i)
	}
	wg.Wait()
}
