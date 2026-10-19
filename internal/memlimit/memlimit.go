// Package memlimit 让 Go 运行时知道本进程能用多少内存。
//
// # 为什么需要
//
// Go 的 GC 默认只看 GOGC：堆涨到上次存活量的两倍才收。它不知道机器有多大，
// 也不知道同机还跑着别的进程。一台 3.4G 的机器上跑三个服务进程，每个都按
// 「存活 ×2 + 尖峰」去要内存，彼此看不见，总和一过物理内存就进 swap ——
// 缺页 IO 把磁盘打满，而 CPU 几乎是空的（2026-10 阿里云单机就是这样死的）。
//
// systemd 的 MemoryHigh / MemoryMax 给每个服务划了 cgroup 上限，但那只是内核
// 那一侧：内核到线了才回收、才杀，Go 运行时并不知道，照样按 GOGC 涨。这个包把
// cgroup 上限读出来，换算成 debug.SetMemoryLimit 的软上限，让 GC 在撞线之前就
// 收紧。
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
	"strconv"
	"strings"
)

// Ratio 软上限占 cgroup 上限的比例。剩下的 10% 留给 Go 运行时不计入的部分
// （mmap 的文件页、cgo、线程栈）以及 GC 收紧到位之前的那一段过冲。
const Ratio = 0.9

// v1Unlimited cgroup v1 不设上限时 limit_in_bytes 是一个接近 2^63 的页对齐值。
const v1Unlimited = 1 << 62

// Result Apply 做了什么。Limit 为 0 表示没有设置。
type Result struct {
	Source string // "GOMEMLIMIT" / "cgroup" / ""
	Cgroup uint64 // 读到的 cgroup 上限（字节），0 = 没有
	Limit  int64  // 设给运行时的软上限（字节），0 = 没设
}

// Apply 按 cgroup 上限设置运行时的软内存上限。每个进程调一次即可。
func Apply() Result {
	if v, ok := os.LookupEnv("GOMEMLIMIT"); ok && v != "" {
		return Result{Source: "GOMEMLIMIT", Limit: debug.SetMemoryLimit(-1)}
	}
	self, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return Result{}
	}
	max := FromCgroup("/", self)
	if max == 0 {
		return Result{}
	}
	limit := int64(float64(max) * Ratio)
	debug.SetMemoryLimit(limit)
	return Result{Source: "cgroup", Cgroup: max, Limit: limit}
}

// FromCgroup 返回本进程所在 cgroup 的有效内存上限（字节），没有上限时回 0。
//
// root 是文件系统根（测试里换成临时目录）；self 是 /proc/self/cgroup 的内容。
// v2 从本进程的 cgroup 一路往上走到根，取每一层 memory.high 与 memory.max 里
// 最小的那个 —— systemd 可能把上限设在服务单元上，也可能设在它所在的 slice 上。
// 没有 v2 时按 v1 的 memory 控制器读 memory.limit_in_bytes。
func FromCgroup(root string, self []byte) uint64 {
	v2Path, v1Path, hasV1 := parseSelf(self)

	base := filepath.Join(root, "sys", "fs", "cgroup")
	if _, err := os.Stat(filepath.Join(base, "cgroup.controllers")); err == nil {
		var best uint64
		for p := path.Clean("/" + v2Path); ; p = path.Dir(p) {
			dir := filepath.Join(base, filepath.FromSlash(p))
			for _, name := range []string{"memory.high", "memory.max"} {
				if v := readLimit(filepath.Join(dir, name)); v != 0 && (best == 0 || v < best) {
					best = v
				}
			}
			if p == "/" {
				break
			}
		}
		return best
	}

	if !hasV1 {
		return 0
	}
	memRoot := filepath.Join(base, "memory")
	// 容器里 /proc/self/cgroup 给的是宿主机视角的路径，挂进来的却是自己那一层：
	// 先按路径找，找不到再读挂载点本身。
	for _, dir := range []string{filepath.Join(memRoot, filepath.FromSlash(v1Path)), memRoot} {
		if v := readLimit(filepath.Join(dir, "memory.limit_in_bytes")); v != 0 {
			return v
		}
	}
	return 0
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

// readLimit 读一个上限文件。文件不存在、内容是 "max" 或 v1 的"无上限"值都回 0。
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
