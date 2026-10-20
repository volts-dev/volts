package memory

// cgroup.go —— 内核那一侧的预算：从 cgroup 读上限与用量（Linux）。
//
// 独占：线设在本进程自己的 cgroup（服务单元）上 —— 软上限 = 线 × Ratio。
// 共享：线设在上层（例如同机所有服务共用的 vs.slice）—— 各进程不互相通信，把**内核
// 的账本**当共享状态，按自己占的比例分剩下的空间（SharedLimit）。

import (
	"bufio"
	"bytes"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// v1Unlimited cgroup v1 不设上限时 limit_in_bytes 是一个接近 2^63 的页对齐值。
const v1Unlimited = 1 << 62

// Budget 从 cgroup 读到的上限。
type Budget struct {
	Own        uint64 // 本进程自己那一层的上限，0 = 没有
	Shared     uint64 // 上层（多个进程共用）的上限，0 = 没有
	SharedDir  string // Shared 所在的 cgroup 目录（读 memory.current / memory.stat）
	SharedName string // 那一层的 cgroup 路径，日志用
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
