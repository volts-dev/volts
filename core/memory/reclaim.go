package memory

// reclaim.go —— 缓存登记与回收。
//
// 缓存在这里登记一个 Reclaimer；controller 每轮判出级别后挨个叫它们丢东西。本包不
// 认识任何具体的缓存，也不知道哪些可以丢 —— 那是登记方的事：
//
//   - **只登记丢了能重算的。** 会话、令牌黑名单、登录限流这类权威状态丢了就是丢数据
//     或丢安全，绝不能登记。
//   - **丢的时候想好重建。** 丢掉之后会被并发请求同时重建的缓存，要么重建走单飞，
//     要么只丢一段时间没人碰的那部分（按租户空闲时间丢冷租户，不动热的）。
//
// 实现方不需要引用本包：方法签名对上就行（cacher 是独立 module，不该依赖 volts）。
// 登记方写一个适配器把它包成 Reclaimer 即可。

import (
	"fmt"
	"runtime/debug"
	"slices"
	"sync"
	"time"
)

// Usage 一个缓存占了多少。Bytes 可以是估算；算不出给 0（回收照样按级别丢，只是监控上
// 看不到字节数）。
type Usage struct {
	Entries int
	Bytes   int64
}

// Reclaimer 一个可以按级别丢东西的缓存。
type Reclaimer interface {
	// String 缓存的名字，日志与监控用；同一进程里应当唯一。
	String() string
	// Usage 当前占用。要便宜：每轮监控都会调。
	Usage() Usage
	// Reclaim 按级别丢东西，返回丢掉了多少（估算）。必须并发安全、不阻塞太久；
	// 不认识的级别按最接近的处理。
	Reclaim(level Level) Usage
}

// Func 用名字和两个函数拼一个 Reclaimer，登记方不必为每个缓存单写一个类型。
func Func(name string, usage func() Usage, reclaim func(Level) Usage) Reclaimer {
	return &funcReclaimer{name: name, usage: usage, reclaim: reclaim}
}

type funcReclaimer struct {
	name    string
	usage   func() Usage
	reclaim func(Level) Usage
}

func (self *funcReclaimer) String() string            { return self.name }
func (self *funcReclaimer) Usage() Usage              { return self.usage() }
func (self *funcReclaimer) Reclaim(level Level) Usage { return self.reclaim(level) }

var (
	reclaimersMu sync.Mutex
	reclaimers   []Reclaimer

	// runMu 串行化回收：controller 的定时回收与 Reclaim（手动 / 控制台下发）不同时跑。
	runMu sync.Mutex
)

// Register 登记一个缓存，返回撤销函数。同一个值登记两次只算一次。
func Register(r Reclaimer) (unregister func()) {
	reclaimersMu.Lock()
	if !slices.Contains(reclaimers, r) {
		reclaimers = append(reclaimers, r)
	}
	reclaimersMu.Unlock()
	return func() {
		reclaimersMu.Lock()
		reclaimers = slices.DeleteFunc(reclaimers, func(x Reclaimer) bool { return x == r })
		reclaimersMu.Unlock()
	}
}

func registered() []Reclaimer {
	reclaimersMu.Lock()
	defer reclaimersMu.Unlock()
	return slices.Clone(reclaimers)
}

// Freed 一个缓存一次回收的结果。
type Freed struct {
	Name  string
	Freed Usage
	Err   error // Reclaim panic 了（已恢复）
}

// Reclaim 立刻按 level 回收一次（不看当前级别、不受 ReclaimEvery 限频），Shed 以上
// 随后把空页还给系统。给运维手动触发用；平时由 controller 自己判。
func Reclaim(level Level, reason string) []Freed {
	out := reclaimAll(level, reason)
	if level >= Shed {
		freeOSMemory()
	}
	return out
}

var (
	freeMu   sync.Mutex
	lastFree time.Time
)

// freeOSMemory 把空出来的页还给系统，限频 FreeOSMemoryEvery。
//
// 缓存丢掉的只是对象；不做这一步，空页要等后台 scavenger 慢慢还，这段时间里进程占的
// 物理内存看起来纹丝不动。debug.FreeOSMemory 自带一次完整 GC，所以限频。
func freeOSMemory() {
	freeMu.Lock()
	defer freeMu.Unlock()
	if time.Since(lastFree) < FreeOSMemoryEvery {
		return
	}
	debug.FreeOSMemory()
	lastFree = time.Now()
}

func reclaimAll(level Level, reason string) []Freed {
	if level <= None {
		return nil
	}
	runMu.Lock()
	defer runMu.Unlock()

	rs := registered()
	out := make([]Freed, 0, len(rs))
	var entries int
	var bytes int64
	start := time.Now()
	for _, r := range rs {
		f := reclaimOne(r, level)
		out = append(out, f)
		entries += f.Freed.Entries
		bytes += f.Freed.Bytes
		if f.Err != nil {
			log.Errf("memory: %s reclaim(%s) panicked: %v", f.Name, level, f.Err)
		}
	}

	recordRun(level, reason, start, entries, bytes)
	if len(rs) > 0 {
		log.Infof("memory: %s (%s) → %d caches freed %d entries / ~%s in %s",
			level, reason, len(rs), entries, formatBytes(bytes), time.Since(start).Round(time.Millisecond))
	} else {
		log.Infof("memory: %s (%s), no cache registered", level, reason)
	}
	return out
}

func reclaimOne(r Reclaimer, level Level) (f Freed) {
	f.Name = r.String()
	defer func() {
		if p := recover(); p != nil {
			f.Err = fmt.Errorf("%v", p)
		}
	}()
	f.Freed = r.Reclaim(level)
	return f
}

// ── 状态快照 ─────────────────────────────

// CacheStatus 一个已登记缓存的占用。
type CacheStatus struct {
	Name  string
	Usage Usage
}

// Status 本进程内存管理的当前状态，监控上报用。
type Status struct {
	Source string // 同 Result.Source
	Limit  int64  // 当前软上限，0 = 没设
	// Reserve 按 EnvReserve 给系统与别的进程留的余量（字节，按当前物理内存折算），
	// 0 = 没设。主机可用内存跌破它，说明这台机器已经装不下现在这些进程。
	Reserve int64
	// PausedUntil 自动回收暂停到什么时候（PauseReclaim），零值 = 没暂停。
	PausedUntil time.Time
	// ReserveOverrideUntil 临时余量（OverrideReserve）到什么时候，零值 = 没有；有时
	// Reserve 是临时的那个、Source 带「(override)」。
	ReserveOverrideUntil time.Time
	Mine                 uint64 // Go 运行时占的内存
	Live                 uint64 // 存活堆
	Level                Level  // 当前回收级别
	Pressure             Pressure

	LastRun       time.Time // 上一次回收（定时或手动）
	LastRunLevel  Level
	LastRunReason string
	LastFreed     Usage

	Caches []CacheStatus
}

var (
	statusMu sync.Mutex
	status   Status
)

// publish controller 每轮结束时更新快照。
func publish(c *controller, s sample) {
	statusMu.Lock()
	now := time.Now()
	paused, _, overrideUntil := controlNow(now)
	status.Source = c.sourceNow(now)
	status.Limit = c.effectiveLimit()
	status.Reserve = 0
	if r := c.reserveNow(now); r.set() && s.hostOK {
		status.Reserve = int64(r.bytes(s.host.Total))
	}
	status.PausedUntil = paused
	status.ReserveOverrideUntil = overrideUntil
	status.Mine = s.mine
	status.Live = s.live
	status.Level = c.level
	status.Pressure = s.pressure
	statusMu.Unlock()
}

func recordRun(level Level, reason string, at time.Time, entries int, bytes int64) {
	statusMu.Lock()
	status.LastRun = at
	status.LastRunLevel = level
	status.LastRunReason = reason
	status.LastFreed = Usage{Entries: entries, Bytes: bytes}
	statusMu.Unlock()
}

// Current 当前状态。各缓存的占用在这里现取（Usage 应当便宜）。
func Current() Status {
	statusMu.Lock()
	s := status
	statusMu.Unlock()
	for _, r := range registered() {
		s.Caches = append(s.Caches, CacheStatus{Name: r.String(), Usage: r.Usage()})
	}
	return s
}
