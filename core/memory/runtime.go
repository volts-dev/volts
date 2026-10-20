package memory

import "runtime/metrics"

// sample 一轮的全部读数。读数和判定分开，判定（controller.step / softLimit）可以
// 直接拿构造的 sample 测。
type sample struct {
	mine       uint64 // 本进程 Go 运行时从系统拿着的内存（与 GOMEMLIMIT 同口径）
	live       uint64 // 上一次 GC 结束时的存活堆
	limiterSeq uint64 // GC CPU 限流器最近一次开启时的 GC 轮次，0 = 从没开过
	shared     uint64 // cgroup 共享那一层的工作集，0 = 没有 / 读不到
	host       Host
	hostOK     bool
	pressure   Pressure
}

func readSample(c *controller) sample {
	s := sample{shared: sharedUsage(c.budget.SharedDir)}
	readRuntime(&s)
	s.host, s.hostOK = readHost()
	s.pressure = readPressure(c.budget.SharedDir)
	return s
}

// readRuntime 走 runtime/metrics，不 STW（ReadMemStats 会停住整个进程）。
func readRuntime(s *sample) {
	m := []metrics.Sample{
		{Name: "/memory/classes/total:bytes"},
		{Name: "/memory/classes/heap/released:bytes"},
		{Name: "/gc/heap/live:bytes"},
		{Name: "/gc/limiter/last-enabled:gc-cycle"},
	}
	metrics.Read(m)
	u := func(i int) uint64 {
		if m[i].Value.Kind() != metrics.KindUint64 {
			return 0
		}
		return m[i].Value.Uint64()
	}
	if total, released := u(0), u(1); total > released {
		s.mine = total - released
	}
	s.live = u(2)
	s.limiterSeq = u(3)
}
