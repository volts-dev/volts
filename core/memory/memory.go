// Package memory 让 Go 运行时知道本进程能用多少内存，并在内存吃紧时让缓存让路。
//
// # 为什么需要
//
// Go 的 GC 默认只看 GOGC：堆涨到上次存活量的两倍才收。它不知道机器有多大，
// 也不知道同机还跑着别的进程。一台 3.4G 的机器上跑三个服务进程，每个都按
// 「存活 ×2 + 尖峰」去要内存，彼此看不见，总和一过物理内存就进 swap ——
// 缺页 IO 把磁盘打满，而 CPU 几乎是空的（2026-10 阿里云单机就是这样死的）。
//
// # 两件事
//
//  1. **软上限**：把「能用多少」换成 debug.SetMemoryLimit。预算来源按优先级取第一个：
//     - 显式的 GOMEMLIMIT 环境变量（包括 GOMEMLIMIT=off）—— 运行时启动时已读过，这里
//     不改它；
//     - cgroup 上限（cgroup.go）：本层是独占，上层（vs.slice）是同机共享；
//     - EnvReserve（VOLTS_MEMORY_RESERVE）：给系统和别的进程**留多少余量**，按主机
//     可用内存算（host.go）。这是不依赖 systemd 的跨平台形态；与 cgroup 同时有时取小的。
//     都没有就不设软上限。
//  2. **回收**（reclaim.go）：缓存经 Register 登记一个 Reclaimer。每 Interval 看一次
//     存活堆占软上限多少、GC 限流有没有开、系统报不报内存压力（pressure.go），定出一个
//     级别（Trim / Shed / Purge），按级别让各缓存丢东西，Shed 以上丢完把空页还给系统。
//
// # 各进程之间
//
// 不互相通信。同机的「大家」用操作系统自己的账本：有 slice 时是那一层的 memory.current，
// 否则是整机的可用内存。没有守护进程、不走网络、崩溃不留残迹；不同用户、不同方式起的
// 进程都看得见同一本账。
//
// # 只是软的
//
// 软上限让 GC 更勤，缓存回收让存活堆变小 —— 都只对「可以不要」的那部分内存有用。
// 请求里正在用的内存（2026-10-19 那次是 715 份同时活着的整租户结果集）它们一点也
// 动不了；在 Linux 上真正兜底的仍是内核的 MemoryMax。
package memory

import (
	"fmt"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"github.com/volts-dev/volts/logger"
)

var log = logger.New("memory")

const (
	// Ratio 软上限占 cgroup 上限的比例。剩下的 10% 留给 Go 运行时不计入的部分
	// （mmap 的文件页、cgo、线程栈）以及 GC 收紧到位之前的那一段过冲。
	Ratio = 0.9

	// Interval 重算软上限、判回收级别的间隔。
	Interval = 5 * time.Second

	// MinLimit 软上限的下限。软上限低于存活堆时 Go 会把 GC 的 CPU 封顶在约 50%，
	// 不会死循环，但没必要把一个小进程压到那一步。
	MinLimit = 64 << 20

	// EnvReserve 跨平台的预算：给系统与别的进程留的余量，"800M" / "1G" / "15%"（占物理
	// 内存）/ 字节数。设了就按主机可用内存分软上限（见 HostLimit）。
	EnvReserve = "VOLTS_MEMORY_RESERVE"
)

// Result Apply 做了什么。
type Result struct {
	Source  string // "GOMEMLIMIT" / "cgroup" / "host" / ""；可以是 "cgroup+host"
	Budget  Budget // cgroup 读到的上限
	Reserve string // EnvReserve 的原样值（host 模式）
	Limit   int64  // 当前设给运行时的软上限（字节），0 = 没设
	Err     error  // EnvReserve 写错了；其余照常
}

var applyOnce sync.Once

// Apply 定预算、设软上限，并起后台协程按 Interval 重算软上限与回收级别。每个进程
// 一次（重复调用无效果，返回第一次的结果）。
//
// 没有任何预算来源时也起协程：系统报内存压力时缓存照样让路。
func Apply() Result {
	applyOnce.Do(func() { applied = apply() })
	return applied
}

var applied Result

func apply() Result {
	c := &controller{}
	var r Result

	if v, ok := os.LookupEnv("GOMEMLIMIT"); ok && v != "" {
		c.fixed = debug.SetMemoryLimit(-1)
		r = Result{Source: "GOMEMLIMIT", Limit: c.fixed}
		c.source = r.Source
		go c.loop()
		return r
	}

	if self, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		c.budget = FindBudget("/", self)
	}
	if v := os.Getenv(EnvReserve); v != "" {
		res, err := ParseReserve(v)
		if err != nil {
			r.Err = fmt.Errorf("%s=%q ignored: %w", EnvReserve, v, err)
		} else {
			c.reserve = res
			r.Reserve = v
		}
	}

	switch {
	case (c.budget.Own != 0 || c.budget.Shared != 0) && c.reserve.set():
		r.Source = "cgroup+host"
	case c.budget.Own != 0 || c.budget.Shared != 0:
		r.Source = "cgroup"
	case c.reserve.set():
		r.Source = "host"
	}
	r.Budget = c.budget

	s := readSample(c)
	if lim := c.softLimit(s); lim > 0 {
		debug.SetMemoryLimit(lim)
		c.limit = lim
		r.Limit = lim
	}
	c.source = r.Source
	go c.loop()
	return r
}

// controller 一个进程一个。只在自己的协程里改字段；Status 经 statusMu 读快照。
type controller struct {
	source  string
	budget  Budget
	reserve Reserve
	fixed   int64 // GOMEMLIMIT 给的软上限：不改它，只拿来判级别
	limit   int64 // 当前软上限，0 = 没设

	level      Level
	calmTicks  int
	lastRun    time.Time // 上一次按级别回收的时刻
	limiterSeq uint64    // 上一次看到的 /gc/limiter/last-enabled
}

func (self *controller) loop() {
	t := time.NewTicker(Interval)
	defer t.Stop()
	for range t.C {
		self.tick(readSample(self), time.Now())
	}
}

// tick 一轮：重算软上限、判级别、该回收就回收。
func (self *controller) tick(s sample, now time.Time) {
	if next := self.softLimit(s); next > 0 {
		// 变化不到 2% 不动：SetMemoryLimit 本身便宜，但没必要每 5 秒抖一次。
		if d := next - self.limit; self.limit == 0 || d > self.limit/50 || -d > self.limit/50 {
			debug.SetMemoryLimit(next)
			self.limit = next
		}
	}

	run := self.step(s, now)
	if run != None {
		reclaimAll(run, reasonOf(s, self.effectiveLimit()))
		self.lastRun = now
		if run >= Shed {
			freeOSMemory()
		}
	}
	publish(self, s)
}

// softLimit 按各预算来源算本轮的软上限，取小的；都没有回 0。GOMEMLIMIT 模式回 0
// （不改它）。
func (self *controller) softLimit(s sample) int64 {
	if self.fixed != 0 {
		return 0
	}
	var lim int64
	take := func(v int64) {
		if v > 0 && (lim == 0 || v < lim) {
			lim = v
		}
	}
	if self.budget.Own != 0 {
		take(int64(float64(self.budget.Own) * Ratio))
	}
	if self.budget.Shared != 0 {
		take(SharedLimit(self.budget.Shared, s.shared, s.mine))
	}
	if self.reserve.set() && s.hostOK {
		take(HostLimit(s.host, self.reserve.bytes(s.host.Total), s.mine))
	}
	return lim
}

// effectiveLimit 判级别用的软上限：自己设的，或 GOMEMLIMIT 给的。
func (self *controller) effectiveLimit() int64 {
	if self.fixed > 0 && self.fixed < 1<<62 { // GOMEMLIMIT=off 时运行时回 MaxInt64
		return self.fixed
	}
	return self.limit
}

// step 推进级别，返回本轮要按哪一级回收（None = 不回收）。
//
//   - 升级立刻生效、立刻回收；
//   - 降级要连续 CalmTicks 轮都低于当前级别才降（回差，免得在线上来回抖）；
//   - 级别不变时每 ReclaimEvery 才再回收一次 —— 存活堆要等下一次 GC 才反映出丢掉的
//     东西，每 5 秒丢一刀等于一分钟内把缓存削光。
func (self *controller) step(s sample, now time.Time) Level {
	limiterOn := s.limiterSeq != 0 && s.limiterSeq != self.limiterSeq
	self.limiterSeq = s.limiterSeq

	want := LevelOf(s.live, self.effectiveLimit(), limiterOn, s.pressure)
	switch {
	case want > self.level:
		self.level, self.calmTicks = want, 0
		return want
	case want < self.level:
		if self.calmTicks++; self.calmTicks >= CalmTicks {
			self.level, self.calmTicks = want, 0
		}
	default:
		self.calmTicks = 0
	}
	if self.level != None && now.Sub(self.lastRun) >= ReclaimEvery {
		return self.level
	}
	return None
}

func reasonOf(s sample, limit int64) string {
	r := ""
	if limit > 0 {
		r = fmt.Sprintf("live heap %s / soft limit %s", formatBytes(int64(s.live)), formatBytes(limit))
	}
	if s.pressure.Level != None {
		if r != "" {
			r += ", "
		}
		r += "system pressure " + s.pressure.Detail
	}
	return r
}

func formatBytes(n int64) string {
	const mib = 1 << 20
	if n >= 1<<30 || n <= -(1<<30) {
		return fmt.Sprintf("%.2fGiB", float64(n)/(1<<30))
	}
	if n >= mib || n <= -mib {
		return fmt.Sprintf("%dMiB", n/mib)
	}
	return fmt.Sprintf("%dKiB", n>>10)
}
