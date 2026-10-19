// Package memlimit 让 Go 运行时知道本进程能用多少内存。
//
// # 为什么需要
//
// Go 的 GC 默认只看 GOGC：堆涨到上次存活量的两倍才收。它不知道机器有多大，
// 也不知道同机还跑着别的进程。一台 3.4G 的机器上跑三个服务进程，每个都按
// 「存活 ×2 + 尖峰」去要内存，彼此看不见，总和一过物理内存就进 swap ——
// 缺页 IO 把磁盘打满，而 CPU 几乎是空的（2026-10 阿里云单机就是这样死的）。
//
// # 两种上限
//
// cgroup（systemd 的 MemoryHigh / MemoryMax）是内核那一侧的线：到线了才回收、
// 才杀，Go 运行时并不知道。这个包把线读出来，换成 debug.SetMemoryLimit 的软上限。
//
//   - **独占**：线设在本进程自己的 cgroup（服务单元）上 —— 软上限 = 线 × Ratio，
//     启动时设一次。
//   - **共享**：线设在上层（例如同机所有服务共用的 vs.slice）—— 这条线是大家的，
//     谁也不能独占。各进程不互相通信，把**内核的账本**当共享状态：每 Interval 读
//     一次那一层的 memory.current（扣掉可回收的 inactive_file），按自己占的比例分
//     剩下的空间：
//
//     软上限 = 自己 + (预算×Ratio − 大家) × 自己/大家
//
//     各进程的软上限加起来正好是 预算×Ratio。谁冲高（catalog 全量发布），剩下的
//     空间就变小，别的进程的 GC 跟着收紧；冲高的那个也不会被拒绝分配 —— 软上限
//     只让 GC 更勤，不是硬墙。
//
// 两种都有时取小的。
//
// # 优先级
//
// 显式的 GOMEMLIMIT 环境变量永远赢（包括 GOMEMLIMIT=off）—— 运行时启动时已经
// 读过它，这里什么都不做。没有 cgroup 上限（本机开发、没配的部署）也什么都不做。
package memlimit

import (
	"bufio"
	"bytes"
	"os"
	"path"
	"path/filepath"
	"runtime/debug"
	"runtime/metrics"
	"strconv"
	"strings"
	"time"
)

// Ratio 软上限占 cgroup 上限的比例。剩下的 10% 留给 Go 运行时不计入的部分
// （mmap 的文件页、cgo、线程栈）以及 GC 收紧到位之前的那一段过冲。
const Ratio = 0.9

// Interval 共享预算下重算软上限的间隔。
const Interval = 5 * time.Second

// MinLimit 共享预算下软上限的下限。软上限低于存活堆时 Go 会把 GC 的 CPU 封顶在
// 约 50%，不会死循环，但没必要把一个小进程压到那一步。
const MinLimit = 64 << 20

// v1Unlimited cgroup v1 不设上限时 limit_in_bytes 是一个接近 2^63 的页对齐值。
const v1Unlimited = 1 << 62

// Budget 从 cgroup 读到的上限。
type Budget struct {
	Own        uint64 // 本进程自己那一层的上限，0 = 没有
	Shared     uint64 // 上层（多个进程共用）的上限，0 = 没有
	SharedDir  string // Shared 所在的 cgroup 目录（读 memory.current / memory.stat）
	SharedName string // 那一层的 cgroup 路径，日志用
}

// Result Apply 做了什么。
type Result struct {
	Source string // "GOMEMLIMIT" / "cgroup" / ""
	Budget Budget
	Limit  int64 // 当前设给运行时的软上限（字节），0 = 没设
}

// Apply 按 cgroup 上限设置运行时的软内存上限；共享预算时起一个后台协程按
// Interval 重算。每个进程调一次。
func Apply() Result {
	if v, ok := os.LookupEnv("GOMEMLIMIT"); ok && v != "" {
		return Result{Source: "GOMEMLIMIT", Limit: debug.SetMemoryLimit(-1)}
	}
	self, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return Result{}
	}
	b := FindBudget("/", self)
	if b.Own == 0 && b.Shared == 0 {
		return Result{}
	}
	limit := b.limit(goMemory(), sharedUsage(b.SharedDir))
	debug.SetMemoryLimit(limit)
	if b.Shared != 0 {
		go b.loop(limit)
	}
	return Result{Source: "cgroup", Budget: b, Limit: limit}
}

func (self Budget) loop(last int64) {
	t := time.NewTicker(Interval)
	defer t.Stop()
	for range t.C {
		next := self.limit(goMemory(), sharedUsage(self.SharedDir))
		// 变化不到 2% 不动：SetMemoryLimit 本身便宜，但没必要每 5 秒抖一次。
		if d := next - last; d > last/50 || -d > last/50 {
			debug.SetMemoryLimit(next)
			last = next
		}
	}
}

// limit 按上限与当前用量算软上限。mine = 本进程 Go 运行时占的内存；used = 共享
// 那一层所有进程的用量（读不到时为 0）。
func (self Budget) limit(mine, used uint64) int64 {
	var lim int64
	if self.Own != 0 {
		lim = int64(float64(self.Own) * Ratio)
	}
	if self.Shared != 0 {
		s := SharedLimit(self.Shared, used, mine)
		if lim == 0 || s < lim {
			lim = s
		}
	}
	return lim
}

// SharedLimit 共享预算下本进程的软上限：自己 + 剩余空间 × 自己/大家。
// 剩余为负（已经超了）时结果小于自己的用量，GC 会立刻收紧。
func SharedLimit(budget, used, mine uint64) int64 {
	target := float64(budget) * Ratio
	if used == 0 || mine == 0 {
		return int64(target)
	}
	if mine > used { // 账本口径不同（memory.current 有延迟）时别让份额超过 1
		used = mine
	}
	lim := float64(mine) + (target-float64(used))*float64(mine)/float64(used)
	if lim < MinLimit {
		lim = MinLimit
	}
	return int64(lim)
}

// goMemory 本进程 Go 运行时从系统拿着的内存（总映射减去已还给系统的堆页），
// 与 GOMEMLIMIT 的口径一致。走 runtime/metrics，不 STW。
func goMemory() uint64 {
	s := []metrics.Sample{
		{Name: "/memory/classes/total:bytes"},
		{Name: "/memory/classes/heap/released:bytes"},
	}
	metrics.Read(s)
	if s[0].Value.Kind() != metrics.KindUint64 || s[1].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return s[0].Value.Uint64() - s[1].Value.Uint64()
}

// sharedUsage 共享那一层当前的工作集：memory.current − inactive_file。
// 不扣的话，页缓存（日志、mmap 的段文件）会把账本填满，各进程的 GC 白白收紧，
// 而那些页内核随时可以回收。
func sharedUsage(dir string) uint64 {
	if dir == "" {
		return 0
	}
	cur := readLimit(filepath.Join(dir, "memory.current"))
	if cur == 0 {
		return 0
	}
	if b, err := os.ReadFile(filepath.Join(dir, "memory.stat")); err == nil {
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			if v, ok := strings.CutPrefix(sc.Text(), "inactive_file "); ok {
				if n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64); err == nil && n < cur {
					cur -= n
				}
				break
			}
		}
	}
	return cur
}

// FindBudget 从本进程所在的 cgroup 读上限。
//
// root 是文件系统根（测试里换成临时目录）；self 是 /proc/self/cgroup 的内容。
// v2：本进程那一层的 memory.high / memory.max 取小的算 Own；再往上走，第一个设了
// 上限的祖先算 Shared（systemd 的 slice）。v1 只读本层的 memory.limit_in_bytes 当
// Own（v1 下各层的用量口径不一致，不做共享）。
func FindBudget(root string, self []byte) Budget {
	v2Path, v1Path, hasV1 := parseSelf(self)

	base := filepath.Join(root, "sys", "fs", "cgroup")
	if _, err := os.Stat(filepath.Join(base, "cgroup.controllers")); err == nil {
		var b Budget
		start := path.Clean("/" + v2Path)
		for p := start; ; p = path.Dir(p) {
			dir := filepath.Join(base, filepath.FromSlash(p))
			if v := dirLimit(dir); v != 0 {
				if p == start {
					b.Own = v
				} else if b.Shared == 0 {
					b.Shared, b.SharedDir, b.SharedName = v, dir, p
				}
			}
			if p == "/" || b.Shared != 0 {
				break
			}
		}
		return b
	}

	if !hasV1 {
		return Budget{}
	}
	memRoot := filepath.Join(base, "memory")
	// 容器里 /proc/self/cgroup 给的是宿主机视角的路径，挂进来的却是自己那一层：
	// 先按路径找，找不到再读挂载点本身。
	for _, dir := range []string{filepath.Join(memRoot, filepath.FromSlash(v1Path)), memRoot} {
		if v := readLimit(filepath.Join(dir, "memory.limit_in_bytes")); v != 0 {
			return Budget{Own: v}
		}
	}
	return Budget{}
}

// dirLimit 一层 cgroup 的 memory.high 与 memory.max 里小的那个，都没设回 0。
func dirLimit(dir string) uint64 {
	var best uint64
	for _, name := range []string{"memory.high", "memory.max"} {
		if v := readLimit(filepath.Join(dir, name)); v != 0 && (best == 0 || v < best) {
			best = v
		}
	}
	return best
}

// parseSelf 从 /proc/self/cgroup 取 v2 统一层级的路径（"0::/..."）与 v1 memory
// 控制器的路径（"N:memory:/..." 或 "N:...,memory,...:/..."）。
func parseSelf(self []byte) (v2 string, v1 string, hasV1 bool) {
	sc := bufio.NewScanner(bytes.NewReader(self))
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), ":", 3)
		if len(parts) != 3 {
			continue
		}
		if parts[0] == "0" && parts[1] == "" {
			v2 = parts[2]
			continue
		}
		for _, c := range strings.Split(parts[1], ",") {
			if c == "memory" {
				v1, hasV1 = parts[2], true
			}
		}
	}
	return v2, v1, hasV1
}

// readLimit 读一个数值文件。文件不存在、内容是 "max" 或 v1 的"无上限"值都回 0。
func readLimit(file string) uint64 {
	b, err := os.ReadFile(file)
	if err != nil {
		return 0
	}
	s := strings.TrimSpace(string(b))
	if s == "" || s == "max" {
		return 0
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil || v >= v1Unlimited {
		return 0
	}
	return v
}
