package etcd

import (
	"errors"
	"testing"

	"github.com/volts-dev/volts/registry"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// etcd 把同一 revision 区间的多条变更打成一个 WatchResponse。Next 必须把它们逐条交完，
// 而不是交出第一条就把整个响应扔掉 —— 被扔掉的服务直到下一轮全量刷新前路由都是旧的。
func TestWatcherNext_DeliversEveryEventInABatch(t *testing.T) {
	put := func(name string, rev int64) *clientv3.Event {
		return &clientv3.Event{Type: clientv3.EventTypePut, Kv: &mvccpb.KeyValue{
			Key:            []byte(servicePath(name) + "/n"),
			Value:          []byte(encode(&registry.Service{Name: name, Version: "1"})),
			CreateRevision: rev, ModRevision: rev, Version: 1,
		}}
	}
	del := &clientv3.Event{Type: clientv3.EventTypeDelete,
		Kv:     &mvccpb.KeyValue{Key: []byte(servicePath("gone") + "/n")},
		PrevKv: &mvccpb.KeyValue{Value: []byte(encode(&registry.Service{Name: "gone", Version: "1"}))}}
	// 删除事件的 PrevKv 在历史被压缩后可能为空：跳过它，不许 panic。
	delNoPrev := &clientv3.Event{Type: clientv3.EventTypeDelete,
		Kv: &mvccpb.KeyValue{Key: []byte(servicePath("x") + "/n")}}

	ch := make(chan clientv3.WatchResponse, 2)
	ch <- clientv3.WatchResponse{Events: []*clientv3.Event{put("sale", 5), delNoPrev, del, put("crm", 5)}}
	close(ch)
	ew := &etcdWatcher{stop: make(chan bool, 1), w: ch}

	var got []string
	for {
		res, err := ew.Next()
		if err != nil {
			break
		}
		got = append(got, res.Action+":"+res.Service.Name)
	}
	want := []string{"create:sale", "delete:gone", "create:crm"}
	if len(got) != len(want) {
		t.Fatalf("一个响应里的事件没有逐条交出：got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 条 = %q, want %q（顺序也要保持）", i, got[i], want[i])
		}
	}
}

// 历史被压缩是自愈的常态：要以 registry.ErrWatchHistoryLost 交出去，调用方据此只记 warn、
// 重建 watcher，而不是当故障报 ERROR（日志中心里每次压缩每个进程一条）。
func TestWatcherNext_CompactionIsWatchHistoryLost(t *testing.T) {
	ch := make(chan clientv3.WatchResponse, 1)
	ch <- clientv3.WatchResponse{CompactRevision: 42, Canceled: true}
	ew := &etcdWatcher{stop: make(chan bool, 1), w: ch}

	_, err := ew.Next()
	if !errors.Is(err, registry.ErrWatchHistoryLost) {
		t.Fatalf("压缩应映射为 ErrWatchHistoryLost，得到 %v", err)
	}
}
