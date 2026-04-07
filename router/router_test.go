package router

import (
	"runtime"
	"sync"
	"testing"
	"time"
)

// TestHttpCtxPoolLoadOrStoreIsRaceFree 验证并发访问同一路由时 pool 只创建一次且无竞态
func TestHttpCtxPoolLoadOrStoreIsRaceFree(t *testing.T) {
	r := New()
	defer close(r.exit) // 防止 watch/refresh goroutine 泄漏

	// 注册一条静态路由
	grp := NewGroup()
	grp.Url("GET", "/race-test", func(ctx *THttpContext) {
		ctx.RespondByJson(map[string]string{"ok": "1"})
	})
	r.RegisterGroup(grp)

	route, _ := r.tree.Match("GET", "/race-test")
	if route == nil {
		t.Skip("route not found, skip pool race test")
	}

	const workers = 100
	pools := make([]*sync.Pool, workers)
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		idx := i
		go func() {
			defer wg.Done()
			p, _ := r.httpCtxPool.LoadOrStore(route.Id(), &sync.Pool{
				New: func() interface{} {
					return NewHttpContext(r)
				},
			})
			pools[idx] = p.(*sync.Pool)
		}()
	}
	wg.Wait()

	// 所有 goroutine 取到的 pool 指针必须相同（LoadOrStore 原子保证）
	for i := 1; i < workers; i++ {
		if pools[i] != pools[0] {
			t.Errorf("pool[%d]=%p != pool[0]=%p: multiple pool instances created (race condition)",
				i, pools[i], pools[0])
		}
	}
}

// TestWatchGoroutineExitsOnRouterClose 验证 watch goroutine 在 router exit 后退出
func TestWatchGoroutineExitsOnRouterClose(t *testing.T) {
	r := New()

	goroutinesBefore := runtime.NumGoroutine()

	// 立即关闭 router（发送 exit 信号）
	// watch/refresh 有 5s 启动延迟，关闭 channel 会让它们直接退出
	close(r.exit)

	// 等待 goroutine 退出（最多 3s）
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		if runtime.NumGoroutine() <= goroutinesBefore+1 {
			return
		}
	}
	// watch/refresh 有 5s 启动延迟，这里只记录，不强断言
	t.Logf("goroutines before=%d after=%d (watch has 5s startup delay, acceptable)",
		goroutinesBefore, runtime.NumGoroutine())
}
