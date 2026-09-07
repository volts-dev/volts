package registry

import (
	"testing"
	"time"

	"github.com/volts-dev/volts/config"
)

// TestUse_DoesNotRegisterThrowawayConfig 锁定：Use 里的临时 Config 不污染全局配置树。
//
// 曾经的缺陷：Use 建一个临时 Config 来解析 Addrs 以便查实例缓存，然后 cfg.Init(opts...)
// 应用 options —— 而那时的 WithConfigPrefixName 内部直接注册配置。于是每调一次
// Use，就把一个随即被丢弃的临时对象注册进全局配置树。命中实例缓存时更糟：
// 后面没有第二次注册去覆盖它，该配置路径便永久指向一个没人持有的孤儿对象 ——
// 改那里的配置不会影响真正在跑的 registry 实例，且毫无提示。
//
// 现在 option 只写字段、绑定只在实例构造时发生一次；本测试钉住这条性质。
// 判据用 Origin：临时 cfg 一旦被绑定，其默认值就会被灌进全局树的 defaults 层，
// 该路径的来源随即从「未知」变成「默认值」。
func TestUse_DoesNotRegisterThrowawayConfig(t *testing.T) {
	const prefix = "throwaway_probe"
	probe := "registry." + prefix + ".timeout"

	if src := config.Default().Origin(probe).Source; src != config.SourceUnknown {
		t.Skipf("探针路径已被占用（来源 %v），换个前缀再测", src)
	}

	// 反复 Use 一个未注册的后端：走不到实例创建，只会跑临时 cfg 那段
	for i := 0; i < 3; i++ {
		Use("no_such_backend_for_test",
			WithName(prefix),
			Addrs("127.0.0.1:2379"),
		)
	}

	if src := config.Default().Origin(probe).Source; src != config.SourceUnknown {
		t.Errorf("Use 把临时 Config 注册进了全局配置树：%s 的来源变成了 %v", probe, src)
	}
}

// TestDivergence_OnlyReportsExplicitDifferences 锁定：只在调用方显式设过且不一致时报分歧。
//
// 复用实例时「先创建者的配置生效、后来者被静默丢弃」是 registry 的固有语义
// （一个实例只有一份运行期状态）。这个语义改不了，但必须让它可见 —— 否则就是
// 「改对了地方也可能不生效」，最难排查的一类。
//
// 同时不能滥报：零值意味着调用方没表态，不是「要求设成 0」。把未表态当成分歧，
// 会让每个不传 option 的持有者都触发一次告警，噪音淹没真问题。
func TestDivergence_OnlyReportsExplicitDifferences(t *testing.T) {
	got := &Config{Name: "etcd", Timeout: 5 * time.Second, TTL: 60 * time.Second}

	cases := []struct {
		name string
		want *Config
		n    int
	}{
		{"配置完全一致", &Config{Timeout: 5 * time.Second, TTL: 60 * time.Second}, 0},
		{"调用方未表态（全零值）", &Config{}, 0},
		{"只有 timeout 不同", &Config{Timeout: 100 * time.Millisecond}, 1},
		{"timeout 与 ttl 都不同", &Config{Timeout: time.Second, TTL: time.Second}, 2},
		{"secure 未开启不算分歧", &Config{Secure: false}, 0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if diffs := divergence(c.want, got); len(diffs) != c.n {
				t.Errorf("分歧条数不对: got %d %v, want %d", len(diffs), diffs, c.n)
			}
		})
	}

	if diffs := divergence(nil, got); diffs != nil {
		t.Errorf("nil 输入应返回 nil: %v", diffs)
	}
}
