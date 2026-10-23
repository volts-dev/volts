package registry

import (
	"errors"
	"sync"
	"testing"
)

// stoppableWatcher：Stop 会让 Next 返回（chanWatcher 的 Stop 是空的，c.Stop() 会一直等缓存的 watch 协程）。
type stoppableWatcher struct {
	ch   chan *Result
	once sync.Once
}

func (w *stoppableWatcher) Next() (*Result, error) {
	if res, ok := <-w.ch; ok {
		return res, nil
	}
	return nil, ErrWatcherStopped
}
func (w *stoppableWatcher) Stop() { w.once.Do(func() { close(w.ch) }) }

type freshBackend struct {
	storeRegistry
	sw *stoppableWatcher
}

func (r *freshBackend) Watcher(...WatchOptions) (Watcher, error) { return r.sw, nil }

// Fresh 绕过缓存读后端并写回缓存：router 的 watch 手里的事件比缓存新时用它（router.servicesAfterEvent）。
// 场景照 10-23 真栈：缓存里还是只有 rpc 节点的那份，后端已经有了 http 节点。
func TestCacherFresh_ReadsBackendAndUpdatesCache(t *testing.T) {
	back := &freshBackend{sw: &stoppableWatcher{ch: make(chan *Result)}}
	c := NewCacher(back).(*registryCacher)
	defer c.Stop()

	_ = back.Register(&Service{Name: "website", Version: "latest", Nodes: []*Node{{Id: "rpc-1", Address: "127.0.0.1:46399"}}})
	if got, err := c.GetService("website"); err != nil || nodeCount(got) != 1 {
		t.Fatalf("前提：缓存里先有 rpc 那一份，得到 %v %v", got, err)
	}
	// 后端多了 http 节点，缓存的 watch 还没送到。
	_ = back.Register(&Service{Name: "website", Version: "latest", Nodes: []*Node{{Id: "http-1", Address: "127.0.0.1:36399"}}})
	if got, _ := c.GetService("website"); nodeCount(got) != 1 {
		t.Fatalf("前提：TTL 内 GetService 应仍是旧的那份，得到 %d 个节点", nodeCount(got))
	}

	got, err := c.Fresh("website")
	if err != nil || nodeCount(got) != 2 {
		t.Fatalf("Fresh 应读到后端的 2 个节点，得到 %v %v", got, err)
	}
	if again, _ := c.GetService("website"); nodeCount(again) != 2 {
		t.Errorf("Fresh 之后缓存应是新的那份，得到 %d 个节点", nodeCount(again))
	}

	// 后端没了：回 ErrNotFound，缓存一起清掉。
	back.mu.Lock()
	back.recs = nil
	back.mu.Unlock()
	if _, err := c.Fresh("website"); !errors.Is(err, ErrNotFound) {
		t.Errorf("后端没有时应回 ErrNotFound，得到 %v", err)
	}
	c.RLock()
	_, cached := c.cache["website"]
	c.RUnlock()
	if cached {
		t.Error("后端没有时缓存应清掉")
	}
}

func nodeCount(ss []*Service) int {
	n := 0
	for _, s := range ss {
		n += len(s.Nodes)
	}
	return n
}
