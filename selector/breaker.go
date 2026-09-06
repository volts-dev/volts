package selector

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/volts-dev/volts/registry"
)

// 节点健康度追踪 —— 失败节点摘除与熔断。
//
// 此前 registrySelector.Mark/Reset 是空函数，框架**完全没有失败节点摘除能力**：
// 一个已经挂掉的后端会被持续选中，直到注册中心的 TTL 过期（分钟级）才消失。
// 期间每一个落到它头上的请求都必然失败。
//
// 这里实现一个按节点计的熔断器：
//   - 连续失败达到 Failures 次 → 熔断，冷却期内不再被选中
//   - 冷却期满自动放行（半开）：下一个请求即为探针，成功则清零，失败则再次熔断
//   - 连续熔断的冷却时间指数退避，上限 MaxCooldown，避免对长期宕机的节点频繁试探
//
// 关键取舍：**全部节点都被熔断时 fail-open**（放行全部节点）。一个把 100% 容量
// 摘掉的熔断器，比它所要防范的故障更糟 —— 那等于把"部分请求失败"升级成
// "服务完全不可用"。

const (
	// DefaultBreakerFailures 触发熔断所需的连续失败次数。
	DefaultBreakerFailures = 3
	// DefaultBreakerCooldown 首次熔断的冷却时长。
	DefaultBreakerCooldown = 10 * time.Second
	// DefaultBreakerMaxCooldown 指数退避后的冷却时长上限。
	DefaultBreakerMaxCooldown = 2 * time.Minute

	// healthPurgeInterval 清理陈旧健康度条目的最小间隔。
	healthPurgeInterval = 5 * time.Minute
	// healthEntryTTL 超过此时长未被访问的条目视为陈旧（节点已下线）。
	healthEntryTTL = 10 * time.Minute
)

type (
	// nodeHealth 是单个节点的健康度状态。
	// 全部字段用原子量：Select（读）与 Mark（写）在不同的请求 goroutine 上并发。
	nodeHealth struct {
		failures  atomic.Uint32 // 连续失败次数，成功即清零
		trips     atomic.Uint32 // 连续熔断次数，用于计算退避
		openUntil atomic.Int64  // 熔断结束时刻(UnixNano)，<=now 表示可用
		lastSeen  atomic.Int64  // 最后一次 Mark 的时刻(UnixNano)，用于清理
	}

	// healthTracker 保存所有节点的健康度。
	healthTracker struct {
		states sync.Map // key: service\x00nodeId → *nodeHealth

		purgeMu   sync.Mutex
		lastPurge time.Time
	}
)

func healthKey(service, nodeId string) string {
	return service + "\x00" + nodeId
}

func (self *healthTracker) get(service, nodeId string) *nodeHealth {
	key := healthKey(service, nodeId)
	if v, ok := self.states.Load(key); ok {
		return v.(*nodeHealth)
	}
	v, _ := self.states.LoadOrStore(key, &nodeHealth{})
	return v.(*nodeHealth)
}

// mark 记录一次调用结果。err 为 nil 视为成功。
func (self *healthTracker) mark(service string, nodeId string, err error, cfg *Config) {
	h := self.get(service, nodeId)
	now := time.Now()
	h.lastSeen.Store(now.UnixNano())

	if err == nil {
		// 成功即完全恢复：清零失败计数、退避档位与熔断状态。
		h.failures.Store(0)
		h.trips.Store(0)
		h.openUntil.Store(0)
		return
	}

	// 三个参数一次读齐，见 Config.breakerSettings：分次读会横跨一次热重载，
	// 拿到新阈值配旧冷却时长这种组合。
	breakerFailures, breakerCooldown, breakerMaxCooldown := cfg.breakerSettings()

	failures := h.failures.Add(1)
	threshold := uint32(breakerFailures)
	if threshold == 0 || failures < threshold {
		return
	}

	// 达到阈值：熔断，冷却时长按连续熔断次数指数退避。
	trips := h.trips.Add(1)

	// 两个零值必须兜底，否则熔断会**静默失效**：cooldown 算成 0 → openUntil
	// 等于当下 → 节点立刻又可选，看起来"熔断器开着"其实什么都没做。
	// 经 newConfig 构造时这两个字段都有默认值，但调用方直接写 Config{} 就会踩到，
	// 而一个悄悄不工作的安全机制比没有更糟 —— 它让人以为自己有保护。
	cooldown := breakerCooldown
	if cooldown <= 0 {
		cooldown = DefaultBreakerCooldown
	}

	// 未声明上限 = 不做指数退避，保持基础冷却时长（而不是"上限为 0"）。
	maxCooldown := breakerMaxCooldown
	if maxCooldown <= 0 {
		maxCooldown = cooldown
	}

	for i := uint32(1); i < trips && cooldown < maxCooldown; i++ {
		cooldown *= 2
	}
	if cooldown > maxCooldown {
		cooldown = maxCooldown
	}

	h.openUntil.Store(now.Add(cooldown).UnixNano())
	// 计数清零，使冷却期满后的第一个请求成为干净的探针：
	// 它再失败一次就会重新熔断（trips 未清零，故退避继续加长）。
	h.failures.Store(0)

	log.Warnf("节点 %s/%s 连续失败 %d 次，熔断 %v", service, nodeId, failures, cooldown)
}

// available 判定节点当前是否可被选中。
func (self *healthTracker) available(service, nodeId string, now int64) bool {
	key := healthKey(service, nodeId)
	v, ok := self.states.Load(key)
	if !ok {
		return true // 从未失败过
	}
	return v.(*nodeHealth).openUntil.Load() <= now
}

// reset 清空某个服务的全部健康度状态。service 为空则清空全部。
func (self *healthTracker) reset(service string) {
	prefix := service + "\x00"
	self.states.Range(func(k, _ any) bool {
		if service == "" || strings.HasPrefix(k.(string), prefix) {
			self.states.Delete(k)
		}
		return true
	})
}

// purge 清理长期未被访问的条目 —— 节点下线后其状态不该无限驻留。
// 按 healthPurgeInterval 限频，故可以在 Select 的热路径上调用。
func (self *healthTracker) purge() {
	self.purgeMu.Lock()
	if time.Since(self.lastPurge) < healthPurgeInterval {
		self.purgeMu.Unlock()
		return
	}
	self.lastPurge = time.Now()
	self.purgeMu.Unlock()

	deadline := time.Now().Add(-healthEntryTTL).UnixNano()
	self.states.Range(func(k, v any) bool {
		if v.(*nodeHealth).lastSeen.Load() < deadline {
			self.states.Delete(k)
		}
		return true
	})
}

// filterHealthy 摘掉处于熔断状态的节点。
//
// 按 svc.Name 而非调用方传入的服务名建键：Select 场景下二者恒等
// （GetService(service) 返回的就是该名字的服务），而 Match 场景一次可以返回
// 多个不同服务，只有按各自的 Name 才对得上 Mark 写入的键。
//
// 返回的是**副本**而非就地改写 service.Nodes。当前 registry cacher 的
// GetService 返回的是 Copy() 深拷贝，就地改并不会污染缓存，但这层依赖是隐式的
// —— 过滤器不该假设上游一定给了它可写的数据，上游换个实现就会静默出错。
//
// 全部节点都被熔断时原样返回并告警 —— 见文件头关于 fail-open 的说明。
func (self *healthTracker) filterHealthy(services []*registry.Service) []*registry.Service {
	now := time.Now().UnixNano()

	out := make([]*registry.Service, 0, len(services))
	total, kept := 0, 0

	for _, svc := range services {
		nodes := make([]*registry.Node, 0, len(svc.Nodes))
		for _, node := range svc.Nodes {
			total++
			if self.available(svc.Name, node.Id, now) {
				nodes = append(nodes, node)
				kept++
			}
		}
		if len(nodes) == 0 {
			continue
		}
		cp := *svc
		cp.Nodes = nodes
		out = append(out, &cp)
	}

	if total > 0 && kept == 0 {
		// fail-open：宁可把请求发给可能已恢复的节点，也不制造一次完全不可用。
		log.Warnf("%d 个候选节点全部处于熔断状态，本次放行全部节点(fail-open)", total)
		return services
	}

	return out
}
