package memory

// pressure.go —— 回收级别，以及它从哪来：本进程离软上限多近、GC 限流开没开、
// 系统报不报内存压力。
//
// 系统的压力信号往往比我们自己算出来的早：别的进程（pg、编译、另一个服务）把机器吃满
// 时，本进程离自己的软上限可能还远。各平台的信号：
//
//   - Linux：PSI（有共享 cgroup 时读那一层的 memory.pressure，否则 /proc/pressure/memory），
//     some / full 是过去 10 秒里有任务 / 全部任务卡在等内存上的时间占比。
//   - macOS：kern.memorystatus_vm_pressure_level（1 正常 / 2 警告 / 4 危急），就是系统
//     发给 App 的那个内存警告。
//   - Windows：GlobalMemoryStatusEx 的 dwMemoryLoad（物理内存使用率）。没有"等待时间"
//     这种信号，只给到 Shed。

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Level 回收级别。Reclaimer 按级别决定丢多少。
type Level int

const (
	// None 不回收。
	None Level = iota
	// Trim 丢过期的和最冷的一小部分（约 1/4）。存活堆到了软上限的 TrimAt。
	Trim
	// Shed 丢一段时间没碰过的全部。存活堆到了 ShedAt、GC 限流开了，或系统报警告。
	Shed
	// Purge 能丢的全丢。存活堆已经顶到软上限，或系统报危急。
	//
	// ★ 丢掉之后会被并发请求同时重建的缓存（冷着的时候一批请求各自重读整张表）不要在
	// Purge 下全丢 —— 那正是 2026-10-19 把机器拖死的形状。这类缓存把 Purge 当 Shed 处理。
	Purge
)

func (self Level) String() string {
	switch self {
	case None:
		return "none"
	case Trim:
		return "trim"
	case Shed:
		return "shed"
	case Purge:
		return "purge"
	}
	return fmt.Sprintf("level(%d)", int(self))
}

const (
	// TrimAt / ShedAt / PurgeAt 存活堆占软上限的比例。
	TrimAt  = 0.80
	ShedAt  = 0.90
	PurgeAt = 1.00

	// CalmTicks 连续这么多轮低于当前级别才降级。
	CalmTicks = 3
	// ReclaimEvery 级别不变时两次回收的最短间隔。
	ReclaimEvery = 30 * time.Second
	// FreeOSMemoryEvery 两次 debug.FreeOSMemory 的最短间隔（它自带一次完整 GC）。
	FreeOSMemoryEvery = 30 * time.Second
)

// Pressure 系统报的内存压力。
type Pressure struct {
	Level  Level
	Detail string // "psi some=12.3 full=0.0" / "macOS warn" / "load 93%"，日志与监控用
}

// LevelOf 本轮该在哪一级。live = 存活堆，limit = 软上限（0 = 没设，只看系统压力）。
func LevelOf(live uint64, limit int64, limiterOn bool, p Pressure) Level {
	l := None
	if limit > 0 && live > 0 {
		switch r := float64(live) / float64(limit); {
		case r >= PurgeAt:
			l = Purge
		case r >= ShedAt:
			l = Shed
		case r >= TrimAt:
			l = Trim
		}
	}
	if limiterOn {
		// GC 已经吃到运行时给它封的 CPU 顶（约 50%）：软上限压在存活堆上了，光靠 GC
		// 收不回来。
		l = max(l, Shed)
	}
	return max(l, p.Level)
}

// PSI 阈值（avg10，百分比）。
const (
	psiTrimSome  = 10
	psiShedSome  = 25
	psiShedFull  = 5
	psiPurgeFull = 20
)

// parsePSI 解析 PSI 文件（"some avg10=1.23 avg60=… total=…" / "full avg10=…"）。
func parsePSI(b []byte) (Pressure, bool) {
	var some, full float64
	var ok bool
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		v, has := strings.CutPrefix(f[1], "avg10=")
		if !has {
			continue
		}
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "some":
			some, ok = n, true
		case "full":
			full = n
		}
	}
	if !ok {
		return Pressure{}, false
	}
	p := Pressure{Detail: fmt.Sprintf("psi some=%.1f full=%.1f", some, full)}
	switch {
	case full >= psiPurgeFull:
		p.Level = Purge
	case some >= psiShedSome || full >= psiShedFull:
		p.Level = Shed
	case some >= psiTrimSome:
		p.Level = Trim
	}
	return p, true
}

// darwinPressure kern.memorystatus_vm_pressure_level 的值换成级别。
func darwinPressure(v uint32) Pressure {
	switch {
	case v >= 4:
		return Pressure{Level: Purge, Detail: "macOS critical"}
	case v >= 2:
		return Pressure{Level: Shed, Detail: "macOS warn"}
	}
	return Pressure{}
}

// windowsPressure dwMemoryLoad 换成级别。
func windowsPressure(load uint32) Pressure {
	switch {
	case load >= 95:
		return Pressure{Level: Shed, Detail: fmt.Sprintf("load %d%%", load)}
	case load >= 90:
		return Pressure{Level: Trim, Detail: fmt.Sprintf("load %d%%", load)}
	}
	return Pressure{}
}
