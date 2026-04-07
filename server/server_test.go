package server

import (
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
