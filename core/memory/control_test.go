package memory

import (
	"errors"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

func resetControl(t *testing.T) {
	t.Helper()
	clear := func() {
		PauseReclaim(0)
		_, _ = OverrideReserve(Reserve{}, 0)
	}
	clear()
	prev := debug.SetMemoryLimit(-1)
	t.Cleanup(func() {
		clear()
		debug.SetMemoryLimit(prev)
	})
}

// 临时余量：没配余量的进程也进 host 模式，来源带「(override)」；到期后自己失效，
// 软上限还原成不设。
func TestOverrideReserve_AppliesAndExpires(t *testing.T) {
	resetControl(t)
	c := &controller{}
	s := sample{mine: 500 * mib, hostOK: true, host: Host{Total: 4000 * mib, Available: 1000 * mib}}
	now := time.Now()
	if got := c.softLimitAt(s, now); got != 0 {
		t.Fatalf("没有来源不该有软上限：%d", got)
	}

	until, err := OverrideReserve(Reserve{Percent: 20}, time.Hour)
	if err != nil || until.Before(now.Add(59*time.Minute)) {
		t.Fatalf("until=%v err=%v", until, err)
	}
	want := HostLimit(s.host, 800*mib, s.mine)
	if got := c.softLimitAt(s, now); got != want {
		t.Fatalf("临时余量下软上限 %d，want %d", got, want)
	}
	if src := c.sourceNow(now); src != "host (override)" {
		t.Fatalf("来源：%q", src)
	}
	c.tick(s, now)
	if c.limit != want {
		t.Fatalf("tick 应设上软上限：%d", c.limit)
	}
	if st := Current(); st.Reserve != 800*mib || st.ReserveOverrideUntil.IsZero() {
		t.Fatalf("快照：%+v", st)
	}

	later := until.Add(time.Second)
	if got := c.softLimitAt(s, later); got != 0 {
		t.Fatalf("到期后不该还有软上限：%d", got)
	}
	c.tick(s, later)
	if c.limit != 0 || debug.SetMemoryLimit(-1) < 1<<62 {
		t.Fatalf("到期后软上限应还原成不设：c.limit=%d runtime=%d", c.limit, debug.SetMemoryLimit(-1))
	}
}

// 有配置来源时算出 0（一时读不到）不动原来的软上限 —— 只有本来没来源才还原。
func TestTick_KeepsLimitWhenConfiguredSourceBlinks(t *testing.T) {
	resetControl(t)
	c := &controller{source: "cgroup", limit: 700 * mib} // 预算字段这一轮读空
	c.tick(sample{mine: 0, shared: 0}, time.Now())
	if c.limit != 700*mib {
		t.Fatalf("cgroup 来源一时算不出时不该清掉软上限：%d", c.limit)
	}
}

// GOMEMLIMIT 定死了软上限：换余量不起作用，要明说而不是假装成功。
func TestOverrideReserve_RefusedUnderGOMEMLIMIT(t *testing.T) {
	resetControl(t)
	old := applied
	applied = Result{Source: "GOMEMLIMIT"}
	defer func() { applied = old }()
	if _, err := OverrideReserve(Reserve{Bytes: 1 << 30}, time.Hour); !errors.Is(err, ErrFixedLimit) {
		t.Fatalf("err=%v", err)
	}
	// 撤销照样可以。
	if _, err := OverrideReserve(Reserve{}, 0); err != nil {
		t.Fatal(err)
	}
}

// 暂停只停丢缓存：级别照判，恢复后下一轮就回收；手动回收不受影响。
func TestPauseReclaim_StopsOnlyAutomaticReclaim(t *testing.T) {
	resetControl(t)
	fc := &fakeCache{name: "paused-test"}
	defer Register(fc)()

	c := &controller{source: "GOMEMLIMIT", fixed: 1000 * mib}
	now := time.Now()
	PauseReclaim(time.Hour)
	c.tick(sample{live: 950 * mib}, now)
	if fc.calls.Load() != 0 {
		t.Fatal("暂停期间不该自动回收")
	}
	if c.level != Shed {
		t.Fatalf("暂停期间级别照判：%s", c.level)
	}
	if Current().PausedUntil.IsZero() {
		t.Fatal("快照应带暂停到期时刻")
	}
	Reclaim(Trim, "manual")
	if fc.calls.Load() != 1 {
		t.Fatal("手动回收不受暂停影响")
	}

	PauseReclaim(0)
	c.tick(sample{live: 950 * mib}, now.Add(Interval))
	if fc.calls.Load() != 2 || Level(fc.last.Load()) != Shed {
		t.Fatalf("恢复后应立刻按 Shed 回收：calls=%d", fc.calls.Load())
	}
}

// 单个缓存回收：只叫那一个；名字不对说清楚。
func TestReclaimCache(t *testing.T) {
	a := &fakeCache{name: "one-a"}
	b := &fakeCache{name: "one-b"}
	defer Register(a)()
	defer Register(b)()

	f, err := ReclaimCache("one-b", Purge, "manual")
	if err != nil || f.Freed.Entries != 3 || b.calls.Load() != 1 || a.calls.Load() != 0 || Level(b.last.Load()) != Purge {
		t.Fatalf("f=%+v err=%v a=%d b=%d", f, err, a.calls.Load(), b.calls.Load())
	}
	if st := Current(); st.LastRunReason != "manual" || st.LastRunLevel != Purge {
		t.Fatalf("快照应记下这次：%+v", st)
	}
	if _, err := ReclaimCache("nope", Trim, "manual"); !errors.Is(err, ErrNoSuchCache) || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err=%v", err)
	}
}
