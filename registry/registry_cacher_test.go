package registry

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeWatcher：Next() 阻塞直到 Stop() 释放；Stop 故意 sleep 一段，
// 让"Stop 没等待 watch goroutine 真正退出"的 bug 在时间维度上可观测。
type fakeWatcher struct {
	release  chan struct{}
	released bool
	mu       sync.Mutex
}

func newFakeWatcher() *fakeWatcher {
	return &fakeWatcher{release: make(chan struct{})}
}

func (w *fakeWatcher) Next() (*Result, error) {
	<-w.release
	return nil, errors.New("watcher stopped")
}

func (w *fakeWatcher) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.released {
		return
	}
	w.released = true
	// 模拟"真实 watcher 关闭需要时间"（网络断开 / etcd lease revoke 等）
	time.Sleep(150 * time.Millisecond)
	close(w.release)
}

// fakeRegistry：嵌入 nil IRegistry；只实现 Watcher / GetService 等 cacher 路径用到的方法。
type fakeRegistry struct {
	IRegistry
	w *fakeWatcher
}

func (r *fakeRegistry) Init(...Option) error            { return nil }
func (r *fakeRegistry) Config() *Config                 { return &Config{} }
func (r *fakeRegistry) Register(*Service, ...Option) error   { return nil }
func (r *fakeRegistry) Deregister(*Service, ...Option) error { return nil }
func (r *fakeRegistry) GetService(name string) ([]*Service, error) {
	return []*Service{{Name: name}}, nil
}
func (r *fakeRegistry) ListServices() ([]*Service, error) { return nil, nil }
func (r *fakeRegistry) Watcher(...WatchOptions) (Watcher, error) {
	return r.w, nil
}
func (r *fakeRegistry) String() string { return "fake-registry" }

// C12：Stop() 必须等待 run() 与 watch goroutine 真正退出。
// 测试用 fakeWatcher.Stop() 故意 sleep 150ms 来制造可观测的时延：
//   - 修复前：Stop() 不等待，立即返回（耗时 < 50ms）—— 测试 FAIL
//   - 修复后：Stop() wg.Wait() 直到 fakeWatcher.Stop() 完成（耗时 ≥ 150ms）—— 测试 PASS
func TestCacher_StopWaitsForBackgroundGoroutines(t *testing.T) {
	w := newFakeWatcher()
	fr := &fakeRegistry{w: w}

	c := NewCacher(fr).(*registryCacher)

	// 触发 run()：GetService 调用 c.get -> 启动 c.run() goroutine
	if _, err := c.GetService("any"); err != nil {
		t.Fatalf("GetService: %v", err)
	}

	// 等待 run() 真正起来（最多 500ms）—— 检查 c.running 标志
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		c.RLock()
		running := c.running
		c.RUnlock()
		if running {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.RLock()
	running := c.running
	c.RUnlock()
	if !running {
		t.Fatal("run() never started; cacher state precondition broken")
	}

	// 触发 Stop —— 测算同步等待时长
	start := time.Now()
	c.Stop()
	elapsed := time.Since(start)

	// fakeWatcher.Stop 至少 sleep 150ms。如果 Stop() 真的等待了 watch
	// goroutine（goroutine 在 defer 里调用 w.Stop()），elapsed 应 ≥ 100ms。
	// 给个 100ms 阈值留出抖动空间。
	if elapsed < 100*time.Millisecond {
		t.Fatalf("Stop() returned in %v but background goroutine not yet exited (no WaitGroup sync)", elapsed)
	}
}
