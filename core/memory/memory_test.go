package memory

import (
	"runtime"
	"runtime/debug"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseReserve(t *testing.T) {
	for in, want := range map[string]Reserve{
		"800M":       {Bytes: 800 << 20},
		"1.5G":       {Bytes: 3 << 29},
		"512MiB":     {Bytes: 512 << 20},
		"256mb":      {Bytes: 256 << 20},
		"1073741824": {Bytes: 1 << 30},
		"15%":        {Percent: 15},
		" 20 % ":     {Percent: 20},
	} {
		got, err := ParseReserve(in)
		if err != nil || got != want {
			t.Errorf("ParseReserve(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "0", "-1G", "100%", "0%", "12X"} {
		if _, err := ParseReserve(bad); err == nil {
			t.Errorf("ParseReserve(%q) 应当报错", bad)
		}
	}
	if got := (Reserve{Percent: 25}).bytes(4 << 30); got != 1<<30 {
		t.Fatalf("25%% of 4G = %d", got)
	}
}

// 同机几个进程只读整机账本与自己，算出来的软上限加起来不超过「可用 − 余量」再加上
// 它们现在用的 —— 也就是说，我们这组进程最多把机器吃到只剩余量。
func TestHostLimit_GroupNeverEatsTheReserve(t *testing.T) {
	const total, reserve = 3400 * mib, 600 * mib
	mine := []uint64{600 * mib, 390 * mib, 430 * mib} // kylin / websvc / cc
	const others = 700 * mib                          // pg / redis / etcd / 系统
	var used uint64 = others
	for _, m := range mine {
		used += m
	}
	h := Host{Total: total, Available: total - used}

	var sum, ours int64
	for _, m := range mine {
		sum += HostLimit(h, reserve, m)
		ours += int64(m)
	}
	if room := int64(h.Available) - reserve; sum-ours > room {
		t.Fatalf("三个进程的软上限比现用多出 %d，超过了可用−余量 %d", sum-ours, room)
	}
	if sum <= ours {
		t.Fatalf("还有余地时软上限应高于现用：sum=%d ours=%d", sum, ours)
	}
}

// 开发机：别的程序占了大半内存、空闲还有很多时，小进程也要分到像样的余地，
// 而不是按它在整机里那点占比被收得死紧。
func TestHostLimit_SmallProcessGetsMinShare(t *testing.T) {
	h := Host{Total: 32 << 30, Available: 13 << 30}
	got := HostLimit(h, 3<<30, 6*mib)
	if want := int64(6*mib) + int64(float64(10<<30)*MinShare); got != want {
		t.Fatalf("got %s, want %s", formatBytes(got), formatBytes(want))
	}
}

func TestHostLimit_ReserveEatenTightens(t *testing.T) {
	h := Host{Total: 3400 * mib, Available: 300 * mib}
	if got := HostLimit(h, 600*mib, 900*mib); got >= 900*mib {
		t.Fatalf("可用已低于余量，软上限 %d 应低于现用", got)
	}
	if got := HostLimit(Host{Total: 3400 * mib, Available: 10 * mib}, 600*mib, 20*mib); got != MinLimit {
		t.Fatalf("got %d, want MinLimit", got)
	}
	if got := HostLimit(Host{}, 600*mib, 100*mib); got != 0 {
		t.Fatalf("读不到主机内存时不设：got %d", got)
	}
}

func TestSoftLimit_TakesTheSmallestSource(t *testing.T) {
	c := &controller{budget: Budget{Own: 1000 * mib}, reserve: Reserve{Bytes: 600 * mib}}
	s := sample{mine: 500 * mib, hostOK: true, host: Host{Total: 3400 * mib, Available: 700 * mib}}
	host := HostLimit(s.host, 600*mib, s.mine)
	if got := c.softLimit(s); got != host || host >= int64(1000*mib*Ratio) {
		t.Fatalf("got %d, want the host limit %d (below own cap)", got, host)
	}
	// GOMEMLIMIT 给的：不动它。
	if got := (&controller{fixed: 2 << 30, budget: Budget{Own: 1 << 30}}).softLimit(s); got != 0 {
		t.Fatalf("GOMEMLIMIT 模式不该算软上限：got %d", got)
	}
}

func TestLevelOf(t *testing.T) {
	const lim = 1000 * mib
	cases := []struct {
		live    uint64
		limit   int64
		limiter bool
		p       Pressure
		want    Level
	}{
		{500 * mib, lim, false, Pressure{}, None},
		{820 * mib, lim, false, Pressure{}, Trim},
		{920 * mib, lim, false, Pressure{}, Shed},
		{1100 * mib, lim, false, Pressure{}, Purge},
		{500 * mib, lim, true, Pressure{}, Shed},           // GC 限流开了
		{500 * mib, 0, false, Pressure{Level: Shed}, Shed}, // 没有软上限也听系统的
		{920 * mib, lim, false, Pressure{Level: Trim}, Shed},
	}
	for _, c := range cases {
		if got := LevelOf(c.live, c.limit, c.limiter, c.p); got != c.want {
			t.Errorf("LevelOf(%d, %d, %v, %v) = %s, want %s", c.live, c.limit, c.limiter, c.p.Level, got, c.want)
		}
	}
}

// 升级立刻回收；级别不变时 ReclaimEvery 内不再回收；降级要连续 CalmTicks 轮。
func TestControllerStep_EscalatesAtOnceAndCalmsSlowly(t *testing.T) {
	c := &controller{limit: 1000 * mib}
	t0 := time.Unix(1_000_000, 0)
	tick := func(i int, live uint64) Level {
		run := c.step(sample{live: live}, t0.Add(time.Duration(i)*Interval))
		if run != None {
			c.lastRun = t0.Add(time.Duration(i) * Interval)
		}
		return run
	}

	if run := tick(0, 850*mib); run != Trim {
		t.Fatalf("到 TrimAt 应当立刻 trim，got %s", run)
	}
	if run := tick(1, 850*mib); run != None {
		t.Fatalf("级别不变、不到 ReclaimEvery 不该再回收，got %s", run)
	}
	if run := tick(2, 950*mib); run != Shed {
		t.Fatalf("升到 Shed 应当立刻回收，got %s", run)
	}
	for i := 3; i < 3+CalmTicks-1; i++ {
		tick(i, 100*mib)
		if c.level != Shed {
			t.Fatalf("第 %d 轮就降级了（要连续 %d 轮）", i, CalmTicks)
		}
	}
	tick(3+CalmTicks-1, 100*mib)
	if c.level != None {
		t.Fatalf("连续 %d 轮低位后应降到 None，got %s", CalmTicks, c.level)
	}
	n := int(ReclaimEvery/Interval) + 10
	if run := tick(n, 100*mib); run != None {
		t.Fatalf("None 不回收，got %s", run)
	}
	if run := tick(n+1, 850*mib); run != Trim {
		t.Fatalf("got %s", run)
	}
	if run := tick(n+1+int(ReclaimEvery/Interval), 850*mib); run != Trim {
		t.Fatalf("级别不变时隔 ReclaimEvery 应当再 trim 一次，got %s", run)
	}
}

// GC 限流只在「这一轮里新开过」时算数：last-enabled 是历史值，不变就是没再开。
func TestControllerStep_LimiterCountsOnlyWhenItMoves(t *testing.T) {
	c := &controller{limit: 1000 * mib}
	now := time.Unix(1_000_000, 0)
	if run := c.step(sample{live: 100 * mib, limiterSeq: 7}, now); run != Shed {
		t.Fatalf("限流器新开过应当 shed，got %s", run)
	}
	c.lastRun = now
	for i := 1; i <= CalmTicks; i++ {
		c.step(sample{live: 100 * mib, limiterSeq: 7}, now.Add(time.Duration(i)*Interval))
	}
	if c.level != None {
		t.Fatalf("限流器没再开过，应当回落，got %s", c.level)
	}
}

func TestParsePSI(t *testing.T) {
	cases := map[string]Level{
		"some avg10=0.00 avg60=0.00 avg300=0.00 total=0\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n":  None,
		"some avg10=12.50 avg60=3.00 avg300=1.00 total=9\nfull avg10=1.00 avg60=0.00 avg300=0.00 total=1\n": Trim,
		"some avg10=30.00 avg60=3.00 avg300=1.00 total=9\nfull avg10=1.00 avg60=0.00 avg300=0.00 total=1\n": Shed,
		"some avg10=8.00 avg60=3.00 avg300=1.00 total=9\nfull avg10=6.00 avg60=0.00 avg300=0.00 total=1\n":  Shed,
		"some avg10=90.00 avg60=3.00 avg300=1.00 total=9\nfull avg10=40.0 avg60=0.00 avg300=0.00 total=1\n": Purge,
	}
	for in, want := range cases {
		p, ok := parsePSI([]byte(in))
		if !ok || p.Level != want {
			t.Errorf("parsePSI(%q) = %+v, %v; want %s", in, p, ok, want)
		}
	}
	if _, ok := parsePSI([]byte("garbage\n")); ok {
		t.Fatal("读不懂的内容不该算有读数")
	}
}

func TestPlatformPressureMapping(t *testing.T) {
	if darwinPressure(1).Level != None || darwinPressure(2).Level != Shed || darwinPressure(4).Level != Purge {
		t.Fatal("macOS 1/2/4 应对应 none/shed/purge")
	}
	if windowsPressure(80).Level != None || windowsPressure(91).Level != Trim || windowsPressure(97).Level != Shed {
		t.Fatal("Windows 负载映射不对")
	}
}

// 本机真读一次：在支持的平台上主机内存必须读得到，否则 host 模式静默失效。
func TestReadHost_Live(t *testing.T) {
	h, ok := readHost()
	if !ok {
		t.Skip("本平台读不到主机内存")
	}
	if h.Total == 0 || h.Available == 0 || h.Available > h.Total {
		t.Fatalf("读数不合理：%+v", h)
	}
	runtime.GC() // 存活堆是「上一次 GC 结束时」的读数，一次都没收过时是 0
	var s sample
	readRuntime(&s)
	if s.mine == 0 || s.live == 0 {
		t.Fatalf("runtime/metrics 读数为 0：%+v", s)
	}
}

type fakeCache struct {
	name   string
	calls  atomic.Int32
	last   atomic.Int32
	panics bool
}

func (self *fakeCache) String() string { return self.name }
func (self *fakeCache) Usage() Usage   { return Usage{Entries: 10, Bytes: 1000} }
func (self *fakeCache) Reclaim(l Level) Usage {
	self.calls.Add(1)
	self.last.Store(int32(l))
	if self.panics {
		panic("boom")
	}
	return Usage{Entries: 3, Bytes: 300}
}

// 一个缓存 panic 不影响别的缓存，也不把 controller 带走；撤销之后不再被叫。
func TestReclaim_IsolatesPanicsAndUnregisters(t *testing.T) {
	good := &fakeCache{name: "good"}
	bad := &fakeCache{name: "bad", panics: true}
	undoGood := Register(good)
	undoBad := Register(bad)
	Register(good) // 重复登记只算一次
	defer undoBad()

	out := reclaimAll(Shed, "test")
	if len(out) != 2 {
		t.Fatalf("got %d results, want 2", len(out))
	}
	if good.calls.Load() != 1 || Level(good.last.Load()) != Shed {
		t.Fatalf("good 应当按 Shed 被叫一次：calls=%d", good.calls.Load())
	}
	var sawPanic bool
	for _, f := range out {
		if f.Name == "bad" && f.Err != nil {
			sawPanic = true
		}
	}
	if !sawPanic {
		t.Fatal("bad 的 panic 应当记进结果")
	}
	if st := Current(); st.LastRunLevel != Shed || st.LastFreed.Entries != 3 || len(st.Caches) != 2 {
		t.Fatalf("状态快照不对：%+v", st)
	}

	undoGood()
	reclaimAll(Trim, "test")
	if good.calls.Load() != 1 {
		t.Fatal("撤销之后不该再被叫")
	}
	if reclaimAll(None, "test") != nil {
		t.Fatal("None 不回收")
	}
}

// 本机端到端：只设 EnvReserve，在读得到主机内存的平台上应当进 host 模式并设上软上限。
func TestApply_HostReserve_Live(t *testing.T) {
	if _, ok := readHost(); !ok {
		t.Skip("本平台读不到主机内存")
	}
	t.Setenv("GOMEMLIMIT", "")
	t.Setenv(EnvReserve, "10%")
	prev := debug.SetMemoryLimit(-1)
	t.Cleanup(func() { debug.SetMemoryLimit(prev) })

	r := apply() // 不走 Apply 的 once；它起的后台协程留在测试进程里无妨
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	if r.Source != "host" && r.Source != "cgroup+host" {
		t.Fatalf("Source = %q, want host", r.Source)
	}
	if r.Limit <= 0 || debug.SetMemoryLimit(-1) != r.Limit {
		t.Fatalf("软上限没设上：result %d, runtime %d", r.Limit, debug.SetMemoryLimit(-1))
	}
	h, _ := readHost()
	t.Logf("host mode: soft limit %s (host total %s, available %s)", formatBytes(r.Limit), formatBytes(int64(h.Total)), formatBytes(int64(h.Available)))
}
