package etcd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/volts-dev/volts/registry"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"
)

type etcdWatcher struct {
	stop    chan bool
	w       clientv3.WatchChan
	client  *clientv3.Client
	timeout time.Duration

	// pending 是上一个 WatchResponse 里还没交出去的结果。etcd 会把同一 revision
	// 区间的多条变更打成一个响应（一个进程注册/注销时它的 http 与 rpc 两条常在一起）；
	// 此前 Next 交出第一条就返回，同一响应里其余的事件被整批丢掉 —— 被丢的那个服务
	// 直到下一轮全量刷新（默认 60s）之前都是旧的路由。
	pending []*registry.Result
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
	for len(ew.pending) == 0 {
		wresp, ok := <-ew.w
		if !ok {
			return nil, errors.New("could not get next")
		}
		if err := wresp.Err(); err != nil {
			if wresp.CompactRevision != 0 || errors.Is(err, rpctypes.ErrCompacted) {
				return nil, fmt.Errorf("%w: %v", registry.ErrWatchHistoryLost, err)
			}
			return nil, err
		}
		if wresp.Canceled {
			return nil, errors.New("could not get next")
		}
		ew.pending = decodeEvents(wresp.Events)
	}

	res := ew.pending[0]
	ew.pending[0] = nil
	ew.pending = ew.pending[1:]
	return res, nil
}

// decodeEvents 把一个 WatchResponse 里的全部事件按顺序解成结果，解不出或验不过的跳过。
func decodeEvents(events []*clientv3.Event) []*registry.Result {
	var out []*registry.Result
	for _, ev := range events {
		var (
			action  string
			service *registry.Service
		)

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
			action = "delete"
			// get service from prevKv
			if ev.PrevKv != nil {
				service = decode(ev.PrevKv.Value)
			}
		}

		if service == nil {
			continue
		}
		out = append(out, &registry.Result{
			Action:  action,
			Service: service,
		})
	}
	return out
}

func (ew *etcdWatcher) Stop() {
	select {
	case <-ew.stop:
		return
	default:
		close(ew.stop)
	}
}
