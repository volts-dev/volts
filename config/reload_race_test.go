package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// rlHot 模拟一个「字段在请求路径上被读、同时会被热重载改写」的配置模型。
// 真实对应物：router.Config.Recover（每请求一次）、
// selector.Config.BreakerFailures（每次 Select）、
// transport.Config.DialTimeout（每次拨号）。
type rlHot struct {
	Config
	Recover  bool          `field:"recover"`
	Failures int           `field:"failures"`
	Cooldown time.Duration `field:"cooldown"`
}

func (self *rlHot) String() string { return "rlhot" }

// 读侧走访问器：热重载写字段时，读必须被挡在写锁之外。
func (self *rlHot) hot() (bool, int, time.Duration) {
	HotRLock()
	defer HotRUnlock()
	return self.Recover, self.Failures, self.Cooldown
}

// TestReload_ConcurrentReadIsRaceFree 钉住：热重载写模型字段与请求路径读同一字段
// 之间必须同步。
//
// 这条竞态曾是 CLAUDE.md 里明写的未决项 —— reload 已经做到「解码到副本、校验通过
// 才应用、只写真正变化的字段」，于是绝大多数 reload 变成零写入；但一个字段**确实
// 变了**的时候，那次写入与并发读之间仍然没有任何同步。vectors 的 cmd/service.go
// 开着 WithWatcher，所以这是线上活着的竞态。
//
// 必须用 -race 跑才有意义：一个 bool 的撕裂读在主流架构上观察不到，
// 竞态检测器是唯一的判据。
func TestReload_ConcurrentReadIsRaceFree(t *testing.T) {
	fn := "reload_race.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	write(t, path, `{"rlhot":{"recover":true,"failures":3,"cooldown":10000}}`)
	cfg := newIsolated(t, fn)

	m := &rlHot{}
	cfg.Register(m)

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

	// 每一轮都真的改值，确保走的是「有变更 -> 写入」那条路，而不是零写入。
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
