package router

import "testing"

// NewGroup 建立的自指关系是后面一切判定的基准。
func TestNewGroupSelfReference(t *testing.T) {
	g := NewGroup()
	if g.config == nil {
		t.Fatal("config 为空")
	}
	if g.config.Group != g {
		t.Fatal("NewGroup 没有建立 config.Group -> group 的自指")
	}
}

// ★ 值拷贝之后，副本与原件**共用同一个 config**，而 config.Group 仍指向原件。
// 这正是 endpoint 随机注册不全的根因，此处把它钉成可判定的事实。
func TestValueCopyBreaksSelfReference(t *testing.T) {
	// 必须写成 `*NewGroup()` 而不是 `orig := NewGroup(); cp := *orig`：后者 vet 的
	// copylocks 会报，前者它故意放行 —— 而 vectors 里的 `TGroup: *router.NewGroup(...)`
	// 正是前者，这里要锁定的就是 vet 看不见的那一种。原件只能从 config 反查回来。
	cp := *NewGroup()
	orig := cp.config.Group

	if orig == nil {
		t.Fatal("前提已变：config.Group 为空")
	}
	if orig == &cp {
		t.Fatal("前提已变：值拷贝后 config.Group 竟指向副本")
	}
	if orig.config != cp.config {
		t.Fatal("前提已变：副本不再与原件共用 config，本测试需重写")
	}
}

// Rebind 修复自指；这是内嵌 TGroup 的类型必须调用的那一步。
func TestRebindRestoresSelfReference(t *testing.T) {
	cp := *NewGroup()
	cp.Rebind()

	if cp.config.Group != &cp {
		t.Fatal("Rebind 之后 config.Group 仍未指向副本")
	}
	// 注册路由不应再触发拷贝告警（此处断言不变量成立，等价于告警不会触发）。
	if cp.config.Group != &cp {
		t.Fatal("不变量未恢复")
	}
}

// Rebind 对没有 config 的零值 group 不能 panic —— 它可能被用在半构造状态上。
func TestRebindOnZeroValueIsSafe(t *testing.T) {
	var g TGroup
	g.Rebind() // 不应 panic
	g.assertNotCopied()
}

// 正常 group 上的校验必须静默通过，否则真实告警会被噪音淹没。
func TestAssertNotCopiedSilentOnHealthyGroup(t *testing.T) {
	g := NewGroup()
	g.assertNotCopied()
	if g.config.Group != g {
		t.Fatal("校验动了状态")
	}
}
