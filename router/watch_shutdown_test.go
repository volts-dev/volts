package router

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/registry"
)

// stopOnlyWatcher 只在被 Stop 之后才从 Next 返回，返回的就是 etcd watcher 被关掉时那个错。
type stopOnlyWatcher struct{ stopped chan struct{} }

func (w *stopOnlyWatcher) Next() (*registry.Result, error) {
	<-w.stopped
	return nil, errors.New("could not get next")
}

func (w *stopOnlyWatcher) Stop() {
	select {
	case <-w.stopped:
	default:
		close(w.stopped)
	}
}

type stopOnlyRegistry struct {
	echoRegistry
	w       *stopOnlyWatcher
	watched chan struct{} // watch 拿到 watcher 时关上
	once    sync.Once
}

func (r *stopOnlyRegistry) Watcher(...registry.WatchOptions) (registry.Watcher, error) {
	r.once.Do(func() { close(r.watched) })
	return r.w, nil
}

func (r *stopOnlyRegistry) ListServices() ([]*registry.Service, error) { return nil, nil }

// 停服时 router.Close 让 watch 自己 Stop 掉 watcher，Next 随即报错 —— 那是我们自己关的，
// 不许记成「error getting next endpoint」（此前每个进程每次停服一条 ERR）。
func TestWatch_ShutdownDoesNotLogWatcherError(t *testing.T) {
	reg := &stopOnlyRegistry{w: &stopOnlyWatcher{stopped: make(chan struct{})}, watched: make(chan struct{})}
	sink := &warnSink{}
	defer logger.AddSink(sink)()

	r := New(WithRegistry(reg))
	select { // watch 开头先等 5 秒才去拿 watcher
	case <-reg.watched:
	case <-time.After(15 * time.Second):
		t.Fatal("watch 一直没去拿 watcher")
	}
	time.Sleep(100 * time.Millisecond) // 让 watch 协程走进 Next
	r.Close()

	select {
	case <-reg.w.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Close 之后 watcher 一直没被 Stop —— 这条测试没测到停服那一支")
	}
	time.Sleep(200 * time.Millisecond) // 等 Next 返回后的那一支走完
	if e, ok := sink.find("error getting next endpoint"); ok {
		t.Errorf("停服时把自己关掉的 watcher 记成了故障: %s", e.Message)
	}
}
