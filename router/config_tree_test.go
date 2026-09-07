package router

import (
	"testing"

	"github.com/volts-dev/volts/config"
)

// TestConfig_SelectorIsOwnedSubsection 锁定路由器配置在配置树上的形状：
// 路由器是根节 router.<name>，它拥有的选择器是子节 router.<name>.selector ——
// 不再有 <prefix>.router / router.<name>.registry 这类由前缀拼出、随装配顺序漂移的节。
func TestConfig_SelectorIsOwnedSubsection(t *testing.T) {
	r := New(WithName("cfgtree_test"))
	defer r.Close()

	bound := config.Bound()
	var hasRouter bool
	for _, p := range bound {
		switch p {
		case "router.cfgtree_test":
			hasRouter = true
		case "selector.default":
			t.Errorf("被拥有的选择器不该是独立根节: %v", bound)
		}
	}
	if !hasRouter {
		t.Fatalf("路由器应绑在 router.cfgtree_test: %v", bound)
	}

	// 子节的默认值沿路径落进树里，说明它是随路由器一起绑定的
	if o := config.Default().Origin("router.cfgtree_test.selector.breaker_failures"); o.Source != config.SourceDefault {
		t.Errorf("选择器的配置应是 router.cfgtree_test.selector.*: 来源 %v", o.Source)
	}
	// 选择器实例用的正是那份被拥有的配置
	if r.Config().SelectorConfig == nil || r.Config().SelectorConfig.BreakerFailures == 0 {
		t.Errorf("SelectorConfig 应带着选择器的默认值: %+v", r.Config().SelectorConfig)
	}
}
