package server

import (
	"testing"

	"github.com/volts-dev/volts/registry"
)

// 同一路径上的不同方法是两个端点，绝不能合并成一个。
//
// 这条曾经真的坏过：dedupEndpoints 按 Endpoint.Name 去重，而 RouteToEndpiont 在
// Name 为空时填的就是路径 —— GET /web/login（渲染登录页）与 POST /web/login
// （提交认证）于是撞成同一个 key，注册中心里只留下其中一个。别的进程照着这份残缺的
// 端点表建代理路由，结果是登录页打得开、点提交 404。
//
// 更难查的是它**不稳定**：TTree.Endpoints() 遍历的是按方法分桶的 map，谁先被
// append 取决于 map 的随机迭代序，所以同一份代码换次重启就可能从"提交 404"变成
// "登录页 404"。
func TestDedupEndpoints_KeepsSamePathDifferentMethods(t *testing.T) {
	eps := []*registry.Endpoint{
		{Name: "/web/login", Path: "/web/login", Method: []string{"GET"}},
		{Name: "/web/login", Path: "/web/login", Method: []string{"POST"}},
	}
	out := dedupEndpoints(eps)
	if len(out) != 2 {
		t.Fatalf("同路径的 GET 与 POST 被合并了：剩下 %d 个端点，期望 2", len(out))
	}
	got := map[string]bool{}
	for _, e := range out {
		got[e.Method[0]] = true
	}
	if !got["GET"] || !got["POST"] {
		t.Errorf("方法丢失：%v", got)
	}
}

// 真正的重复项（同名服务的多个 group 各登记了一次同一条路由）仍要合并。
func TestDedupEndpoints_DropsTrueDuplicates(t *testing.T) {
	eps := []*registry.Endpoint{
		{Name: "/shop", Path: "/shop", Method: []string{"GET"}},
		{Name: "/shop", Path: "/shop", Method: []string{"GET"}},
		{Name: "/shop/cart", Path: "/shop/cart", Method: []string{"GET"}},
	}
	out := dedupEndpoints(eps)
	if len(out) != 2 {
		t.Fatalf("重复端点没去掉：剩下 %d 个，期望 2", len(out))
	}
	if out[0].Path != "/shop" || out[1].Path != "/shop/cart" {
		t.Errorf("没有保留首次出现的顺序：%q, %q", out[0].Path, out[1].Path)
	}
}

// 多方法登记在同一条路由上（Url([]string{"GET","POST"}, …)）时，Method 切片整体
// 参与比较；它与拆成两条登记不是同一个 key，两者共存也不该互相吃掉。
func TestDedupEndpoints_MultiMethodRouteIsItsOwnKey(t *testing.T) {
	eps := []*registry.Endpoint{
		{Name: "/x", Path: "/x", Method: []string{"GET", "POST"}},
		{Name: "/x", Path: "/x", Method: []string{"GET"}},
	}
	if out := dedupEndpoints(eps); len(out) != 2 {
		t.Fatalf("剩下 %d 个端点，期望 2", len(out))
	}
}

// 路径为空的端点（RPC 侧有此形态）回落到按 Name 去重，行为不变。
func TestDedupEndpoints_FallsBackToNameWhenPathEmpty(t *testing.T) {
	eps := []*registry.Endpoint{
		{Name: "svc.method", Method: []string{"CONNECT"}},
		{Name: "svc.method", Method: []string{"CONNECT"}},
		{Name: "svc.other", Method: []string{"CONNECT"}},
	}
	if out := dedupEndpoints(eps); len(out) != 2 {
		t.Fatalf("剩下 %d 个端点，期望 2", len(out))
	}
}

func TestDedupEndpoints_SkipsNil(t *testing.T) {
	eps := []*registry.Endpoint{
		nil,
		{Name: "/a", Path: "/a", Method: []string{"GET"}},
		nil,
	}
	out := dedupEndpoints(eps)
	if len(out) != 1 || out[0].Path != "/a" {
		t.Fatalf("nil 端点没被跳过：%+v", out)
	}
}
