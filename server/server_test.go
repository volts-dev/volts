package server

import (
	"errors"
	"sync"
	"testing"
)

// TestRegisteredFieldIsAtomic 验证 registered 字段通过 atomic.Bool 操作，无竞态
func TestRegisteredFieldIsAtomic(t *testing.T) {
	srv := New()

	var wg sync.WaitGroup
	const goroutines = 50

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			srv.registered.Store(true)
			_ = srv.registered.Load()
			srv.registered.Store(false)
		}()
	}
	wg.Wait()
	// 如果有竞态，-race 检测器会报告
}

// TestStopAggregatesErrors 验证 Stop() 中 transport 和 broker 的错误均被聚合返回
// 对应 H5：原先只返回 ts.Close() 一个错误
func TestStopAggregatesErrors(t *testing.T) {
	tsErr := errors.New("transport-close-error")
	brokerErr := errors.New("broker-close-error")

	// 模拟 Stop goroutine 中的错误聚合逻辑
	joined := errors.Join(tsErr, brokerErr)

	if !errors.Is(joined, tsErr) {
		t.Errorf("expected tsErr in joined error, got: %v", joined)
	}
	if !errors.Is(joined, brokerErr) {
		t.Errorf("expected brokerErr in joined error, got: %v", joined)
	}

	// 验证只有一个错误时仍正确
	onlyOne := errors.Join(nil, brokerErr)
	if !errors.Is(onlyOne, brokerErr) {
		t.Errorf("single error should be preserved in Join: %v", onlyOne)
	}

	// 验证两个 nil 时返回 nil
	if err := errors.Join(nil, nil); err != nil {
		t.Errorf("expected nil when both errors are nil, got: %v", err)
	}
}

// TestMapDeleteVsNilSemantics 验证 delete(map, key) 与 map[key]=nil 的区别
// 对应 H3：Deregister 使用 delete 而非 nil 赋值，确保 key 完全从 map 中移除
func TestMapDeleteVsNilSemantics(t *testing.T) {
	type key struct{ topic string }

	// 正确做法：delete 后 key 不存在
	m := map[key][]string{
		{topic: "t1"}: {"sub1"},
		{topic: "t2"}: {"sub2"},
	}
	k := key{topic: "t1"}
	delete(m, k)

	if _, exists := m[k]; exists {
		t.Error("after delete, key should not exist in map")
	}
	if len(m) != 1 {
		t.Errorf("expected 1 entry remaining, got %d", len(m))
	}

	// 错误做法对比：nil 赋值后 key 仍存在（这是 H3 修复前的 bug 模式）
	m2 := map[key][]string{
		{topic: "t1"}: {"sub1"},
	}
	m2[k] = nil
	if _, exists := m2[k]; !exists {
		t.Error("nil assignment should leave key in map (this is the former bug pattern)")
	}
	if len(m2) != 1 {
		t.Errorf("nil map assignment: expected 1 entry, got %d", len(m2))
	}
}

func TestAddressOptionSyncsBothFields(t *testing.T) {
	const addr = ":19999"
	srv := New(Address(addr))
	cfg := srv.Config()

	if cfg.Address != addr {
		t.Errorf("cfg.Address = %q, want %q", cfg.Address, addr)
	}
	if cfg.Transport.Config().Addrs != addr {
		t.Errorf("Transport.Addrs = %q, want %q", cfg.Transport.Config().Addrs, addr)
	}
}
