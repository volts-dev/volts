package registry

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type lifecycleRegistry struct {
	IRegistry
	starts   atomic.Int64
	entered  chan struct{}
	stopGate chan struct{}
}
type lifecycleWatcher struct {
	backend *lifecycleRegistry
	once    sync.Once
	exit    chan struct{}
}

func (r *lifecycleRegistry) GetService(name string) ([]*Service, error) {
	return []*Service{{Name: name, Nodes: []*Node{{Id: "node"}}}}, nil
}
func (r *lifecycleRegistry) Watcher(...WatchOptions) (Watcher, error) {
	r.starts.Add(1)
	return &lifecycleWatcher{backend: r, exit: make(chan struct{})}, nil
}
func (w *lifecycleWatcher) Next() (*Result, error) {
	select {
	case w.backend.entered <- struct{}{}:
	default:
	}
	<-w.exit
	return nil, ErrWatcherStopped
}
func (w *lifecycleWatcher) Stop() {
	w.once.Do(func() {
		if w.backend.stopGate != nil {
			<-w.backend.stopGate
		}
		close(w.exit)
	})
}
func awaitLifecycle(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("lifecycle event timed out")
	}
}

func TestCacher_FirstReadsReserveOneRun(t *testing.T) {
	old := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(old)
	r := &lifecycleRegistry{entered: make(chan struct{}, 100)}
	c := NewCacher(r).(*registryCacher)
	t.Cleanup(c.Stop)
	for i := 0; i < 100; i++ {
		if _, err := c.GetService(fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
		c.RLock()
		running := c.running
		c.RUnlock()
		if !running {
			t.Fatal("GetService returned without reserving the run slot")
		}
	}
	awaitLifecycle(t, r.entered)
	time.Sleep(120 * time.Millisecond)
	if n := r.starts.Load(); n != 1 {
		t.Fatalf("first reads started %d watchers, want 1", n)
	}
}

func TestCacher_ConcurrentStopWaitsAndCannotRestart(t *testing.T) {
	gate := make(chan struct{})
	r := &lifecycleRegistry{entered: make(chan struct{}, 2), stopGate: gate}
	c := NewCacher(r).(*registryCacher)
	defer c.Stop()
	var unblock sync.Once
	defer unblock.Do(func() { close(gate) })
	if _, err := c.GetService("a"); err != nil {
		t.Fatal(err)
	}
	awaitLifecycle(t, r.entered)
	first, second := make(chan struct{}), make(chan struct{})
	go func() { c.Stop(); close(first) }()
	deadline := time.Now().Add(time.Second)
	for !c.quit() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !c.quit() {
		t.Fatal("Stop did not close exit")
	}
	go func() { c.Stop(); close(second) }()
	select {
	case <-second:
		t.Fatal("second Stop returned while watcher was still closing")
	case <-time.After(25 * time.Millisecond):
	}
	if _, err := c.GetService("new"); err != ErrWatcherStopped {
		t.Fatalf("stopped cache lookup: %v", err)
	}
	unblock.Do(func() { close(gate) })
	awaitLifecycle(t, first)
	awaitLifecycle(t, second)
	if r.starts.Load() != 1 {
		t.Fatal("read after Stop restarted watcher")
	}
}

func TestCacher_PauseIsInterruptedByStop(t *testing.T) {
	c := NewCacher(NewNopRegistry()).(*registryCacher)
	done := make(chan struct{})
	go func() {
		if c.pause(time.Hour) {
			t.Error("pause completed rather than canceled")
		}
		close(done)
	}()
	c.Stop()
	awaitLifecycle(t, done)
}

func TestCacher_StopRacesFirstRead(t *testing.T) {
	for i := 0; i < 100; i++ {
		r := &lifecycleRegistry{entered: make(chan struct{}, 2)}
		c := NewCacher(r).(*registryCacher)
		done := make(chan struct{})
		go func() { _, _ = c.GetService("a"); close(done) }()
		c.Stop()
		awaitLifecycle(t, done)
		c.Stop()
		c.RLock()
		running := c.running
		c.RUnlock()
		if running {
			t.Fatal("run survived completed Stop")
		}
	}
}
