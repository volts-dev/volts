package router

import (
	"testing"

	"github.com/volts-dev/volts/registry"
)

func TestRouteToEndpoint_FillsFromMeta(t *testing.T) {
	r := newRoute(nil, []string{"GET"}, nil, "/users/:id", "", "", "")
	r.meta = &Operation{
		Summary:  "get user",
		Request:  &Value{Name: "opReq", Type: "opReq"},
		Response: &Value{Name: "opRsp", Type: "opRsp"},
	}
	ep := RouteToEndpiont(r)
	if ep.Request == nil || ep.Request.Type != "opReq" {
		t.Fatalf("endpoint request not filled: %+v", ep.Request)
	}
	if ep.Response == nil || ep.Response.Type != "opRsp" {
		t.Fatalf("endpoint response not filled: %+v", ep.Response)
	}
	if ep.Description != "get user" {
		t.Fatalf("description not filled: %q", ep.Description)
	}
}

// OpMeta 的标记必须进 Endpoint.Metadata——registry 是微服务下唯一的全局路由表，
// 标记不落在这里，别的进程就永远看不到本进程声明了什么。
func TestRouteToEndpoint_CarriesOpMeta(t *testing.T) {
	r := newRoute(nil, []string{"GET"}, nil, "/shop", "", "", "").
		WithOp(OpMeta("list_as_website_content", "Shop"), OpDescription("long"))

	ep := RouteToEndpiont(r)
	if got := ep.Metadata["list_as_website_content"]; got != "Shop" {
		t.Fatalf("op meta not carried into endpoint metadata: %q (%+v)", got, ep.Metadata)
	}
	// 框架自用键不被自由标记挤掉
	if ep.Metadata["description"] != "long" {
		t.Fatalf("framework description key lost: %+v", ep.Metadata)
	}
}

// WithOp 叠加在既有 meta 上，不能把 Api 注册期反射出来的 I/O schema 抹掉。
func TestWithOp_KeepsExistingOperation(t *testing.T) {
	r := newRoute(nil, []string{"GET"}, nil, "/x", "", "", "")
	r.meta = &Operation{Summary: "s", Request: &Value{Name: "req", Type: "req"}}

	r.WithOp(OpMeta("k", "v"))

	op, ok := r.meta.(*Operation)
	if !ok || op.Request == nil || op.Request.Type != "req" {
		t.Fatalf("WithOp dropped existing operation: %+v", r.meta)
	}
	if op.Summary != "s" || op.Meta["k"] != "v" {
		t.Fatalf("WithOp did not merge: %+v", op)
	}
}

// **整链**：Url(...).WithOp(...) 挂的标记，必须能从路由树取回来。
//
// 单测 RouteToEndpiont 只证明"这个 route 对象翻译得对"，证明不了 WithOp 改的
// 就是树里那一个——addRoute 若把 route 复制一份再插树，标记会静默留在返回值上，
// 注册中心里一个字都没有，而所有单元测试照样绿。跨进程读标记的那一侧
// （vectors core/reg.MarkedRoutes）除了"没有标记"什么也看不到，无从查起。
func TestUrlWithOp_MetaReachesTreeEndpoint(t *testing.T) {
	grp := NewGroup()
	grp.Url([]string{"GET"}, "/shop", func(ctx *THttpContext) {}).
		WithOp(OpMeta("list_as_website_content", "Shop"))

	var found *registry.Endpoint
	for _, ep := range grp.tree.AllEndpoints() {
		if ep.Path == "/shop" {
			found = ep
			break
		}
	}
	if found == nil {
		t.Fatal("route not in tree")
	}
	if got := found.Metadata["list_as_website_content"]; got != "Shop" {
		t.Fatalf("mark lost between Url() and the tree: %q (%+v)", got, found.Metadata)
	}
}

// addRoute 在重复注册等情形下返回 nil，链式调用不该因此 panic。
func TestWithOp_NilRouteIsSafe(t *testing.T) {
	var r *route
	if got := r.WithOp(OpMeta("k", "v")); got != nil {
		t.Fatalf("nil route should stay nil: %+v", got)
	}
}

func TestRouteClone_CopiesMeta(t *testing.T) {
	r := newRoute(nil, []string{"GET"}, nil, "/x", "", "", "")
	r.meta = &Operation{Summary: "s"}
	c := r.Clone().(*route)
	if c.meta == nil {
		t.Fatal("clone lost meta")
	}
}
