package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// rlHot 模拟一个「字段在请求路径上被读、同时会被热重载改写」的配置结构体。
// 真实对应物：router.Config.Recover（每请求一次）、selector.Config.BreakerFailures
// （每次 Select）。
type rlHot struct {
	Recover  bool          `field:"recover"`
	Failures int           `field:"failures"`
	Cooldown time.Duration `field:"cooldown"`
}

// 读侧走访问器：热重载写字段时，读必须被挡在写锁之外。
func (self *rlHot) hot() (bool, int, time.Duration) {
	HotRLock()
	defer HotRUnlock()
	return self.Recover, self.Failures, self.Cooldown
}

// TestReload_ConcurrentReadIsRaceFree 钉住：热重载写结构体字段与请求路径读同一字段
// 之间必须同步。必须用 -race 跑才有意义：一个 bool 的撕裂读在主流架构上观察不到。
func TestReload_ConcurrentReadIsRaceFree(t *testing.T) {
	fn := "reload_race.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{"rlhot":{"recover":true,"failures":3,"cooldown":10000}}`)
	cfg := newIsolated(t, fn)

	m := &rlHot{}
	cfg.Bind("rlhot", m)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				m.hot()
			}
		}()
	}

	for i := 0; i < 30; i++ {
		body := `{"rlhot":{"recover":false,"failures":5,"cooldown":20000}}`
		if i%2 == 1 {
			body = `{"rlhot":{"recover":true,"failures":3,"cooldown":10000}}`
		}
		rewriteAndWatch(t, cfg, path, body)
		if changes := cfg.ReloadWithChanges(); len(changes) == 0 {
			t.Fatalf("第 %d 轮没有产生任何变更 —— 这条测试就测不到东西了", i)
		}
	}

	close(stop)
	wg.Wait()
}
