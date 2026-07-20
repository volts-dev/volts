package selector

import (
	"errors"
	"testing"
	"time"

	"github.com/volts-dev/volts/registry"
)

var errBoom = errors.New("boom")

func testConfig() *Config {
	return &Config{
		BreakerFailures:    3,
		BreakerCooldown:    50 * time.Millisecond,
		BreakerMaxCooldown: 400 * time.Millisecond,
	}
}

func svc(name string, nodeIds ...string) *registry.Service {
	s := &registry.Service{Name: name}
	for _, id := range nodeIds {
		s.Nodes = append(s.Nodes, &registry.Node{Id: id, Address: id + ":8080"})
	}
	return s
}

// 收集一组 service 里剩余的节点 id。
func nodeIds(services []*registry.Service) []string {
	var out []string
	for _, s := range services {
		for _, n := range s.Nodes {
			out = append(out, n.Id)
		}
	}
	return out
}

func contains(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// 未达阈值不熔断 —— 偶发失败不应摘节点。
func TestBreakerDoesNotTripBelowThreshold(t *testing.T) {
	h := &healthTracker{}
	cfg := testConfig()

	h.mark("svc", "a", errBoom, cfg)
	h.mark("svc", "a", errBoom, cfg)

	ids := nodeIds(h.filterHealthy([]*registry.Service{svc("svc", "a", "b")}))
	if !contains(ids, "a") {
		t.Fatalf("2 次失败(阈值 3)不应熔断，剩余节点 = %v", ids)
	}
}

// 达到阈值即摘除，这是本次改动的核心：此前 Mark 是空函数，
// 挂掉的节点会被持续选中直到注册中心 TTL 过期（分钟级）。
func TestBreakerTripsAtThreshold(t *testing.T) {
	h := &healthTracker{}
	cfg := testConfig()

	for i := 0; i < 3; i++ {
		h.mark("svc", "a", errBoom, cfg)
	}

	ids := nodeIds(h.filterHealthy([]*registry.Service{svc("svc", "a", "b")}))
	if contains(ids, "a") {
		t.Errorf("连续 3 次失败后节点 a 应被摘除，剩余 = %v", ids)
	}
	if !contains(ids, "b") {
		t.Errorf("健康节点 b 不应受影响，剩余 = %v", ids)
	}
}

// 成功一次即完全恢复，失败计数不得累积到下一轮。
func TestBreakerSuccessResetsFailures(t *testing.T) {
	h := &healthTracker{}
	cfg := testConfig()

	h.mark("svc", "a", errBoom, cfg)
	h.mark("svc", "a", errBoom, cfg)
	h.mark("svc", "a", nil, cfg) // 成功
	h.mark("svc", "a", errBoom, cfg)
	h.mark("svc", "a", errBoom, cfg)

	ids := nodeIds(h.filterHealthy([]*registry.Service{svc("svc", "a", "healthy")}))
	if !contains(ids, "a") {
		t.Fatalf("成功后计数应清零，不该只用 2 次失败就熔断，剩余 = %v", ids)
	}
}

// 冷却期满后自动放行（半开），让下一个请求充当探针。
func TestBreakerRecoversAfterCooldown(t *testing.T) {
	h := &healthTracker{}
	cfg := testConfig()

	for i := 0; i < 3; i++ {
		h.mark("svc", "a", errBoom, cfg)
	}
	if contains(nodeIds(h.filterHealthy([]*registry.Service{svc("svc", "a", "healthy")})), "a") {
		t.Fatal("熔断未生效")
	}

	time.Sleep(cfg.BreakerCooldown + 30*time.Millisecond)

	ids := nodeIds(h.filterHealthy([]*registry.Service{svc("svc", "a", "healthy")}))
	if !contains(ids, "a") {
		t.Fatalf("冷却期满应自动放行，剩余 = %v", ids)
	}
}

// 连续熔断的冷却时长指数退避 —— 长期宕机的节点不该被高频试探。
func TestBreakerBackoffGrows(t *testing.T) {
	h := &healthTracker{}
	cfg := testConfig()

	trip := func() time.Duration {
		for i := 0; i < 3; i++ {
			h.mark("svc", "a", errBoom, cfg)
		}
		open := h.get("svc", "a").openUntil.Load()
		return time.Until(time.Unix(0, open))
	}

	first := trip()
	second := trip()

	if second <= first {
		t.Fatalf("第二次熔断冷却 %v 应长于第一次 %v（指数退避）", second, first)
	}
	if second > cfg.BreakerMaxCooldown {
		t.Fatalf("冷却 %v 超过上限 %v", second, cfg.BreakerMaxCooldown)
	}
}

// 全部节点都熔断时必须 fail-open。
//
// 一个把 100% 容量摘掉的熔断器比它防范的故障更糟 —— 那是把"部分请求失败"
// 升级成"服务完全不可用"。宁可把请求发给可能已恢复的节点。
func TestBreakerFailsOpenWhenAllTripped(t *testing.T) {
	h := &healthTracker{}
	cfg := testConfig()

	for _, id := range []string{"a", "b"} {
		for i := 0; i < 3; i++ {
			h.mark("svc", id, errBoom, cfg)
		}
	}

	ids := nodeIds(h.filterHealthy([]*registry.Service{svc("svc", "a", "b")}))
	if len(ids) != 2 {
		t.Fatalf("全部熔断时应放行全部节点(fail-open)，剩余 = %v", ids)
	}
}

// 过滤必须返回副本 —— 入参来自 registry cacher 的共享缓存，
// 就地改写会污染其他调用方看到的数据。
func TestBreakerFilterDoesNotMutateInput(t *testing.T) {
	h := &healthTracker{}
	cfg := testConfig()

	for i := 0; i < 3; i++ {
		h.mark("svc", "a", errBoom, cfg)
	}

	in := []*registry.Service{svc("svc", "a", "b")}
	h.filterHealthy(in)

	if len(in[0].Nodes) != 2 {
		t.Fatalf("入参被就地改写了，节点数 = %d, want 2", len(in[0].Nodes))
	}
}

// 不同服务的同名节点互不影响。
func TestBreakerIsolatesServices(t *testing.T) {
	h := &healthTracker{}
	cfg := testConfig()

	for i := 0; i < 3; i++ {
		h.mark("svc-a", "n1", errBoom, cfg)
	}

	ids := nodeIds(h.filterHealthy([]*registry.Service{svc("svc-b", "n1")}))
	if !contains(ids, "n1") {
		t.Fatalf("svc-a 的失败不应影响 svc-b 的同名节点，剩余 = %v", ids)
	}
}

// Reset 立即解除熔断。
func TestBreakerReset(t *testing.T) {
	h := &healthTracker{}
	cfg := testConfig()

	for i := 0; i < 3; i++ {
		h.mark("svc", "a", errBoom, cfg)
	}
	h.reset("svc")

	ids := nodeIds(h.filterHealthy([]*registry.Service{svc("svc", "a", "healthy")}))
	if !contains(ids, "a") {
		t.Fatalf("Reset 后应立即恢复，剩余 = %v", ids)
	}
}

// Selector.Mark 在熔断关闭（BreakerFailures=0）时不得有任何副作用。
func TestSelectorMarkNoopWhenBreakerDisabled(t *testing.T) {
	s := &registrySelector{config: &Config{BreakerFailures: 0}}

	for i := 0; i < 10; i++ {
		s.Mark("svc", &registry.Node{Id: "a"}, errBoom)
	}

	if _, ok := s.health.states.Load(healthKey("svc", "a")); ok {
		t.Fatal("熔断关闭时不应记录任何健康度状态")
	}
}

// Mark 传入 nil node 不得 panic —— 调用方拿不到节点时会这么传。
func TestSelectorMarkNilNode(t *testing.T) {
	s := &registrySelector{config: testConfig()}
	s.Mark("svc", nil, errBoom) // 不 panic 即通过
}
