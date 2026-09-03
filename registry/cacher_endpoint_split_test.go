package registry

import "testing"

// 同一个服务名下的两个进程，端点集不同，**不能被并成一条**。
//
// 现实里这不是罕见形状：一个模块被别的进程 blank-import 带起来，那个进程就也
// 注册了同一个服务名，而它编译得早、少几条端点；滚动升级期间新旧二进制并存同理。
// 并成一条的后果是 nodes 全并、endpoints 只剩一份 —— 客户端按合并后的节点轮询，
// 打到不实现该端点的进程上，回一句 `rpc status NotFound: server returned no
// detail`，与"这个部署没装那个模块"长得一模一样。两边日志都是干净的。
//
// 2026-09-03 真栈实测：vectors 的 `calendar` 服务名下有 calendar 本体（223 端点，
// 含 systray）与 blank-import 了它的 hr_svc（222 端点），顶栏活动菜单一次成一次不成。
func TestCacherUpdate_KeepsProcessesWithDifferentEndpointsApart(t *testing.T) {
	c, ok := NewCacher(&hookRegistry{}).(*registryCacher)
	if !ok {
		t.Fatal("NewCacher 没有返回 *registryCacher")
	}

	svc := func(nodeId string, eps ...string) *Service {
		list := make([]*Endpoint, 0, len(eps))
		for _, e := range eps {
			list = append(list, &Endpoint{Name: e})
		}
		return &Service{
			Name: "calendar", Version: "latest", Endpoints: list,
			Nodes: []*Node{{Id: nodeId, Address: nodeId + ":1"}},
		}
	}

	full := svc("calendar-svc", "/calendar.event.read", "/mail.activity.systray_get_activities")
	partial := svc("hr-svc", "/calendar.event.read")

	// 先播种缓存（update 只更新已经被查过的服务）。
	c.Lock()
	c.watched["calendar"] = true
	c.cache["calendar"] = []*Service{full}
	c.Unlock()

	c.update(&Result{Action: "create", Service: partial})

	c.RLock()
	got := c.cache["calendar"]
	c.RUnlock()

	if len(got) != 2 {
		t.Fatalf("端点集不同的两个进程要各成一条，得到 %d 条", len(got))
	}

	// 关键断言：声明了 systray 的那一条，**只**带着实现了它的那个节点。
	var withSystray *Service
	for _, s := range got {
		for _, ep := range s.Endpoints {
			if ep.Name == "/mail.activity.systray_get_activities" {
				withSystray = s
			}
		}
	}
	if withSystray == nil {
		t.Fatal("声明了 systray 的那一条不见了")
	}
	if len(withSystray.Nodes) != 1 || withSystray.Nodes[0].Id != "calendar-svc" {
		t.Fatalf("它不该带上没有这个端点的节点，得到 %v", withSystray.Nodes)
	}
}

// 反过来：端点集**相同**的两个进程（同一份二进制的多个副本，也就是绝大多数情况）
// 照旧合并成一条，节点并进去。这一条保证上面那个改动只在真出问题时才改变形状。
func TestCacherUpdate_StillMergesIdenticalEndpointSets(t *testing.T) {
	c, ok := NewCacher(&hookRegistry{}).(*registryCacher)
	if !ok {
		t.Fatal("NewCacher 没有返回 *registryCacher")
	}
	eps := []*Endpoint{{Name: "/calendar.event.read"}}

	c.Lock()
	c.watched["calendar"] = true
	c.cache["calendar"] = []*Service{{
		Name: "calendar", Version: "latest", Endpoints: eps,
		Nodes: []*Node{{Id: "a", Address: "a:1"}},
	}}
	c.Unlock()

	c.update(&Result{Action: "create", Service: &Service{
		Name: "calendar", Version: "latest", Endpoints: eps,
		Nodes: []*Node{{Id: "b", Address: "b:1"}},
	}})

	c.RLock()
	got := c.cache["calendar"]
	c.RUnlock()

	if len(got) != 1 {
		t.Fatalf("端点集相同就该合并成一条，得到 %d 条", len(got))
	}
	if len(got[0].Nodes) != 2 {
		t.Fatalf("两个节点都该在，得到 %d 个", len(got[0].Nodes))
	}
}
