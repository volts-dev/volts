package memory

// control.go —— 运维的手动操控：暂停自动回收、临时换余量、单个缓存立即回收。
//
// 全部只在内存里、全部带期限：进程重启就回到配置，到期自己失效 —— 不需要谁记得撤销，
// 也不会有「半年前临时调的一个值还在生效」。本包不关心谁来调（平台控制台经 vectors
// core/nodestat 的上报回执下发）。

import (
	"errors"
	"fmt"
	"math"
	"runtime/debug"
	"sync"
	"time"
)

// MaxControlFor 一次操控最长管多久。
const MaxControlFor = 24 * time.Hour

var (
	// ErrFixedLimit GOMEMLIMIT 定死了软上限，换余量不起作用。
	ErrFixedLimit = errors.New("memory: GOMEMLIMIT is set, the soft limit is fixed and a reserve has no effect")
	// ErrNoSuchCache 没有登记这个名字的缓存。
	ErrNoSuchCache = errors.New("memory: no cache registered under that name")
)

var (
	controlMu       sync.Mutex
	pausedUntil     time.Time
	reserveOverride Reserve
	overrideUntil   time.Time
)

func clampFor(d time.Duration) time.Duration { return min(d, MaxControlFor) }

// PauseReclaim 暂停自动回收 d 这么久（d ≤ 0 = 立刻恢复），返回到期时刻。
//
// 只停「按级别丢缓存」，软上限照常设、级别照常判（恢复时不用从头升级）；手动的
// Reclaim / ReclaimCache 不受影响。用在：知道接下来一段时间会有一波大查询，不想让
// 缓存在这时候被反复清掉。
func PauseReclaim(d time.Duration) time.Time {
	controlMu.Lock()
	defer controlMu.Unlock()
	if d <= 0 {
		pausedUntil = time.Time{}
		return time.Time{}
	}
	pausedUntil = time.Now().Add(clampFor(d))
	log.Infof("memory: automatic reclaim paused until %s", pausedUntil.Format(time.RFC3339))
	return pausedUntil
}

// OverrideReserve 临时把主机余量换成 r，管 d 这么久；r 为空或 d ≤ 0 = 撤销。返回到期时刻。
//
// 没配 EnvReserve 的进程也可以临时进 host 模式。下一轮（≤ Interval）生效。
func OverrideReserve(r Reserve, d time.Duration) (time.Time, error) {
	controlMu.Lock()
	defer controlMu.Unlock()
	if !r.set() || d <= 0 {
		reserveOverride, overrideUntil = Reserve{}, time.Time{}
		return time.Time{}, nil
	}
	if applied.Source == "GOMEMLIMIT" {
		return time.Time{}, ErrFixedLimit
	}
	reserveOverride, overrideUntil = r, time.Now().Add(clampFor(d))
	log.Infof("memory: host reserve overridden until %s", overrideUntil.Format(time.RFC3339))
	return overrideUntil, nil
}

// controlNow 此刻生效的操控（到期的当没有）。
func controlNow(now time.Time) (paused time.Time, override Reserve, until time.Time) {
	controlMu.Lock()
	defer controlMu.Unlock()
	if now.Before(pausedUntil) {
		paused = pausedUntil
	}
	if reserveOverride.set() && now.Before(overrideUntil) {
		override, until = reserveOverride, overrideUntil
	}
	return
}

// ReclaimCache 立刻按 level 回收一个缓存（名字即 Reclaimer.String()）。Shed 以上随后把
// 空页还给系统。
func ReclaimCache(name string, level Level, reason string) (Freed, error) {
	if level <= None {
		return Freed{Name: name}, nil
	}
	var target Reclaimer
	for _, r := range registered() {
		if r.String() == name {
			target = r
			break
		}
	}
	if target == nil {
		return Freed{Name: name}, fmt.Errorf("%w: %q", ErrNoSuchCache, name)
	}
	runMu.Lock()
	start := time.Now()
	f := reclaimOne(target, level)
	recordRun(level, reason, start, f.Freed.Entries, f.Freed.Bytes)
	runMu.Unlock()
	if f.Err != nil {
		log.Errf("memory: %s reclaim(%s) panicked: %v", f.Name, level, f.Err)
		return f, f.Err
	}
	log.Infof("memory: %s %s (%s) freed %d entries", name, level, reason, f.Freed.Entries)
	if level >= Shed {
		freeOSMemory()
	}
	return f, nil
}

// reserveNow 此刻生效的余量：临时的优先。
func (self *controller) reserveNow(now time.Time) Reserve {
	if _, r, _ := controlNow(now); r.set() {
		return r
	}
	return self.reserve
}

// sourceNow 带上临时余量的来源说明。
func (self *controller) sourceNow(now time.Time) string {
	if _, r, _ := controlNow(now); !r.set() {
		return self.source
	}
	switch self.source {
	case "":
		return "host (override)"
	case "cgroup":
		return "cgroup+host (override)"
	}
	return self.source + " (override)"
}

// clearLimit 预算来源没了（临时余量到期、原本没配）：把软上限还原成「不设」。
func (self *controller) clearLimit() {
	if self.fixed == 0 && self.limit != 0 {
		debug.SetMemoryLimit(math.MaxInt64)
		self.limit = 0
	}
}
