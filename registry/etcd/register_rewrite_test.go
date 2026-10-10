package etcd

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/volts-dev/volts/registry"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// 连不上本机 etcd 就跳过。不能用 TCP 拨号判可达（本机网络扩展会全部接受），要真发一次请求。
func liveRegistry(t *testing.T) *etcdRegistry {
	t.Helper()
	r := New(registry.Addrs("127.0.0.1:2379"), registry.Timeout(5*time.Second)).(*etcdRegistry)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := r.client.Get(ctx, "/volts-test/probe"); err != nil {
		t.Skipf("本机 etcd 不可用: %v", err)
	}

	// 换到测试专用前缀，绝不碰真栈的 /volts/registry/
	old := prefix
	prefix = fmt.Sprintf("/volts-test/registry-%d/", time.Now().UnixNano())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r.client.Delete(ctx, prefix, clientv3.WithPrefix())
		prefix = old
		r.client.Close()
	})
	return r
}

func keyState(t *testing.T, r *etcdRegistry, key string) (modRev int64, lease int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rsp, err := r.client.Get(ctx, key)
	if err != nil {
		t.Fatalf("读 %s: %v", key, err)
	}
	if len(rsp.Kvs) != 1 {
		t.Fatalf("读 %s: 期望 1 条，得到 %d 条", key, len(rsp.Kvs))
	}
	return rsp.Kvs[0].ModRevision, rsp.Kvs[0].Lease
}

// 内容没变的续租**一个字节都不许写**，内容变了要重写但**沿用原租约**。
//
// 此前两条都不成立：端点顺序随 map 迭代变 → 签名变 → 节点哈希变，于是每 30 秒整条
// Grant + Put；每次还新开一个租约，旧的空挂一个 TTL。2026-10-10 真栈实测稳态 95 秒
// 33 次 PUT、44 个键挂着 279 个租约，云上 etcd 成了写 I/O 第一大户。
func TestRegister_UnchangedWritesNothing_ChangedReusesLease(t *testing.T) {
	r := liveRegistry(t)

	svc := &registry.Service{
		Name:    "rewrite-probe",
		Version: "1",
		Endpoints: []*registry.Endpoint{
			{Name: "a", Path: "/a", Method: []string{"GET"}},
			{Name: "b", Path: "/b", Method: []string{"CONNECT"}},
		},
		Nodes: []*registry.Node{{Id: "n1", Address: "127.0.0.1:1", Metadata: map[string]string{"k": "v"}}},
	}
	ttl := registry.RegisterTTL(30 * time.Second)
	key := nodePath(svc.Name, "n1")

	if err := r.Register(svc, ttl); err != nil {
		t.Fatal(err)
	}
	rev1, lease1 := keyState(t, r, key)
	if lease1 == 0 {
		t.Fatal("带 TTL 注册却没挂租约")
	}

	for i := 0; i < 3; i++ {
		if err := r.Register(svc, ttl); err != nil {
			t.Fatal(err)
		}
	}
	if rev, _ := keyState(t, r, key); rev != rev1 {
		t.Fatalf("内容没变的续租重写了注册项（ModRevision %d → %d）", rev1, rev)
	}

	// 端点变了：必须重写（此前哈希只看节点，没开签名时这一步会被跳过）
	svc.Endpoints = append(svc.Endpoints, &registry.Endpoint{Name: "c", Path: "/c", Method: []string{"POST"}})
	if err := r.Register(svc, ttl); err != nil {
		t.Fatal(err)
	}
	rev2, lease2 := keyState(t, r, key)
	if rev2 == rev1 {
		t.Fatal("端点变了却没有重写注册项")
	}
	if lease2 != lease1 {
		t.Fatalf("内容变了换了新租约（%x → %x）：旧租约会空挂一个 TTL", lease1, lease2)
	}
}

// 租约被撤（etcd 重启 / 过期）之后，下一轮必须重新拿租约写回来。
func TestRegister_LeaseLostRegistersAgain(t *testing.T) {
	r := liveRegistry(t)

	svc := &registry.Service{
		Name:  "lease-lost-probe",
		Nodes: []*registry.Node{{Id: "n1", Address: "127.0.0.1:1"}},
	}
	ttl := registry.RegisterTTL(30 * time.Second)
	key := nodePath(svc.Name, "n1")

	if err := r.Register(svc, ttl); err != nil {
		t.Fatal(err)
	}
	_, lease1 := keyState(t, r, key)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := r.client.Revoke(ctx, clientv3.LeaseID(lease1)); err != nil {
		t.Fatal(err)
	}

	if err := r.Register(svc, ttl); err != nil {
		t.Fatal(err)
	}
	_, lease2 := keyState(t, r, key)
	if lease2 == 0 || lease2 == lease1 {
		t.Fatalf("租约丢了之后没有重新拿租约（%x → %x）", lease1, lease2)
	}
}
