package router

import "testing"

// registry 周期刷新会为同一 (method,path) 生成新 id 的 route 热替换树节点；
// onRouteReplaced 是 TRouter 清理按旧 route id 键控的上下文池的唯一信号，
// 在此钉死触发语义：真替换必回调、hook 合并/同对象重放不回调。
func TestAddRouteReplaceNotifiesOldRoute(t *testing.T) {
	tree := NewRouteTree()
	var replaced []int
	tree.onRouteReplaced = func(old *route) { replaced = append(replaced, old.Id()) }

	r1 := newRoute(nil, []string{"GET"}, nil, "/svc/x", "", "", "")
	r2 := newRoute(nil, []string{"GET"}, nil, "/svc/x", "", "", "")

	if err := tree.AddRoute(r1); err != nil {
		t.Fatal(err)
	}
	if len(replaced) != 0 {
		t.Fatalf("first insert must not fire the hook, got %v", replaced)
	}

	if err := tree.AddRoute(r2); err != nil {
		t.Fatal(err)
	}
	if len(replaced) != 1 || replaced[0] != r1.Id() {
		t.Fatalf("replace must fire with old route id %d, got %v", r1.Id(), replaced)
	}

	// 同一 route 对象重复注册不算替换（无需清池）
	if err := tree.AddRoute(r2); err != nil {
		t.Fatal(err)
	}
	if len(replaced) != 1 {
		t.Fatalf("re-adding the same route object must not fire, got %v", replaced)
	}

	// 现路由必须是 r2
	if got, _ := tree.Match("GET", "/svc/x"); got == nil || got.Id() != r2.Id() {
		t.Fatalf("tree must serve the replacing route")
	}
}
