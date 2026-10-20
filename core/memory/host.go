package memory

// host.go —— 不依赖 cgroup 的预算：按整机可用内存、给系统留一段余量。
//
// 没有 slice 的地方（macOS、Windows、没装 systemd 的机器）内核不替一组进程记账，
// 但它记着整机还剩多少：Linux 的 MemAvailable、macOS 的空闲 + 可回收页、Windows 的
// ullAvailPhys —— 都把随时能回收的页缓存算作可用。各进程只读这一个数和自己：
//
//	软上限 = 自己 + (可用 − 余量) × max(自己 / 整机已用, MinShare)
//
// pg / redis 这些不在本框架里的进程也算进「整机已用」，跟着按占比分剩下的空间。
//
// MinShare 是份额的下限。没有它，一台开发机上浏览器和 IDE 占着 19G 时，一个刚起来的
// 6M 进程分到的是 10G 空闲里的 3M —— 空间明明大把，GC 却被收得死紧，白耗 CPU。有了它
// 的代价是：份额低于 MinShare 的那几个进程在同一个 Interval 里一起冲高时，合起来可能
// 越过余量一点（最多 (可用 − 余量) × MinShare × 个数），下一轮就收回来。余量本身就是
// 给这种过冲留的。

import (
	"fmt"
	"strconv"
	"strings"
)

// MinShare 主机模式下一个进程至少分到剩余空间的这么多（见文件头）。
const MinShare = 1.0 / 8

// Host 整机内存读数（字节）。
type Host struct {
	Total     uint64
	Available uint64 // 不必换出任何人的匿名内存就能腾出来的那部分
}

// Reserve 给系统与别的进程留的余量：固定字节数，或物理内存的百分比。
type Reserve struct {
	Bytes   uint64
	Percent float64 // (0, 100)
}

func (self Reserve) set() bool { return self.Bytes != 0 || self.Percent != 0 }

func (self Reserve) bytes(total uint64) uint64 {
	if self.Percent != 0 {
		return uint64(float64(total) * self.Percent / 100)
	}
	return self.Bytes
}

// ParseReserve 解析 "800M" / "1.5G" / "512MiB" / "15%" / "1073741824"。
func ParseReserve(v string) (Reserve, error) {
	s := strings.TrimSpace(v)
	if p, ok := strings.CutSuffix(s, "%"); ok {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil || f <= 0 || f >= 100 {
			return Reserve{}, fmt.Errorf("percent must be in (0, 100)")
		}
		return Reserve{Percent: f}, nil
	}
	u := strings.ToUpper(s)
	u = strings.TrimSuffix(strings.TrimSuffix(u, "IB"), "B")
	mul := float64(1)
	switch {
	case strings.HasSuffix(u, "K"):
		mul, u = 1<<10, strings.TrimSuffix(u, "K")
	case strings.HasSuffix(u, "M"):
		mul, u = 1<<20, strings.TrimSuffix(u, "M")
	case strings.HasSuffix(u, "G"):
		mul, u = 1<<30, strings.TrimSuffix(u, "G")
	case strings.HasSuffix(u, "T"):
		mul, u = 1<<40, strings.TrimSuffix(u, "T")
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(u), 64)
	if err != nil || f <= 0 {
		return Reserve{}, fmt.Errorf("want a size like 800M / 1G or a percent like 15%%")
	}
	return Reserve{Bytes: uint64(f * mul)}, nil
}

// HostLimit 按整机可用内存算本进程的软上限：自己 + (可用 − 余量) × 份额。
// 余量已经被吃掉（可用 < 余量）时结果小于自己的用量，GC 立刻收紧；不低于 MinLimit。
func HostLimit(h Host, reserve, mine uint64) int64 {
	if h.Total == 0 || h.Available > h.Total {
		return 0
	}
	used := h.Total - h.Available
	room := float64(h.Available) - float64(reserve)
	var lim float64
	switch {
	case mine == 0:
		lim = room
	case used == 0:
		lim = float64(mine) + room
	default:
		if mine > used { // 两本账口径不同时别让份额超过 1
			used = mine
		}
		share := max(float64(mine)/float64(used), MinShare)
		if room < 0 {
			share = float64(mine) / float64(used) // 超了按真实占比收紧，不放大
		}
		lim = float64(mine) + room*share
	}
	if lim < MinLimit {
		lim = MinLimit
	}
	return int64(lim)
}
