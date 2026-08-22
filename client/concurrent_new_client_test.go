package client_test

import (
	"sync"
	"testing"
	"time"

	"github.com/volts-dev/volts/client"
)

// 并发构造 HttpClient 不能死锁。
//
// # 病灶
//
// `client.NewHttpClient` → `transport.NewHTTPTransport` → `transport.newConfig`
// → `config.Register(cfg)`。而 `config.(*Config).Register` 的结构是：
//
//	core.models.Store(cfgStr, cfg)
//	core.applyMu.Lock(); defer Unlock()
//	saveDefaults(...)
//	cfg.Load()                       // ← 这一步会走到 LoadToModel
//
// 而 `LoadToModel` 结尾有一句「尚未注册就连带注册」：
//
//	if _, has := core.models.Load(model.String()); !has { self.Register(model) }
//
// 一旦那个 Load 落空，就是**在持有 applyMu 的情况下再次 Lock 同一把互斥量** ——
// Go 的 sync.Mutex 不可重入，当场自死锁，并且把后续所有 Register 一起堵死。
//
// # 真栈表现（2026-08-22）
//
// vectors 的 `core/pool.NewHttp` 用 sync.Pool 缓存 HttpClient，池命中就不构造，
// 所以平时看不见。同时升级 4 个模块时，4 个 `DownloadResourceFile` 一起 miss、
// 一起构造 client，其中一个踩中递归：
//
//	goroutine 461 [sync.Mutex.Lock, 1 minutes]:
//	  config.(*Config).Register     config.go:313   ← 持锁，调 cfg.Load()
//	   transport.(*Config).Load     transport/config.go:162
//	    config.(*Config).LoadToModel config.go:552  ← self.Register(model)
//	     config.(*Config).Register  config.go:299   ← 二次 Lock，死锁
//
// 另有 6 个 goroutine 排在它后面。表现是模块升级停在 5%「Prefetching module
// data...」永不推进，**一个字的错误日志都没有**，前端进度条转到 15 分钟超时。
func TestNewHttpClient_ConcurrentDoesNotDeadlock(t *testing.T) {
	const goroutines = 32

	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = client.NewHttpClient()
			}()
		}
		wg.Wait()
	}()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		// 不能用 t.Fatal 之后就算了：死锁的 goroutine 还挂着，
		// 打出全量 stack 才看得到是谁在等谁。
		t.Fatalf("并发构造 %d 个 HttpClient 在 20 秒内没有全部返回 —— "+
			"config.Register 自死锁（Register 持 applyMu → cfg.Load() → "+
			"LoadToModel → Register 二次 Lock）。用 -timeout 30s 跑可看到完整 stack。",
			goroutines)
	}
}
