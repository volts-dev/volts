package etcd

import (
	"context"
	"errors"
	"time"

	"github.com/volts-dev/volts/registry"
	clientv3 "go.etcd.io/etcd/client/v3"
)

type etcdWatcher struct {
	stop    chan bool
	w       clientv3.WatchChan
	client  *clientv3.Client
	timeout time.Duration
}

func newEtcdWatcher(r *etcdRegistry, timeout time.Duration, opts ...registry.WatchOptions) (registry.Watcher, error) {
	var wo registry.WatchConfig
	for _, o := range opts {
		o(&wo)
	}

	ctx, cancel := context.WithCancel(context.Background())
	stop := make(chan bool, 1)

	go func() {
		<-stop
		cancel()
	}()

	watchPath := prefix
	if len(wo.Service) > 0 {
		watchPath = servicePath(wo.Service) + "/"
	}

	return &etcdWatcher{
		stop:    stop,
		w:       r.client.Watch(ctx, watchPath, clientv3.WithPrefix(), clientv3.WithPrevKV()),
		client:  r.client,
		timeout: timeout,
	}, nil
}

func (ew *etcdWatcher) Next() (*registry.Result, error) {
	for wresp := range ew.w {
		if wresp.Err() != nil {
			return nil, wresp.Err()
		}
		if wresp.Canceled {
			return nil, errors.New("could not get next")
		}

		var service *registry.Service
		for _, ev := range wresp.Events {
			var action string

			switch ev.Type {
			case clientv3.EventTypePut:
				service = decode(ev.Kv.Value)
				// 验不过的注册项不进缓存（registry.AcceptRecord）。删除事件不验：删掉一条
				// 从没被收下的，对缓存是空操作；而收下过的那条，信任表换过之后可能就验不过了，
				// 拦下它的删除反倒让它在缓存里多活一个 TTL。
				if service != nil && !registry.AcceptRecord(service) {
					service = nil
				}

				if ev.IsCreate() {
					action = "create"
				} else if ev.IsModify() {
					action = "update"
				}
			case clientv3.EventTypeDelete:
				service = nil
				action = "delete"

				// get service from prevKv
				service = decode(ev.PrevKv.Value)
			}

			if service == nil {
				continue
			}
			return &registry.Result{
				Action:  action,
				Service: service,
			}, nil
		}
	}
	return nil, errors.New("could not get next")
}

func (ew *etcdWatcher) Stop() {
	select {
	case <-ew.stop:
		return
	default:
		close(ew.stop)
	}
}
