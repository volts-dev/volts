package server

import (
	"fmt"
	"testing"
)

func TestTree(t *testing.T) {
	tree1 := NewRouteTree()
	tree2 := NewRouteTree()

	// #静态路由#
	tree1.AddRoute("GET", "/aaa", new(TRoute))
	tree2.AddRoute("GET", "/aaa/bbb", new(TRoute))

	////tree1.AddRoute("GET", "/abc1/tree1", r)
	tree2.AddRoute("GET", "/aaa/bbb", new(TRoute))

	// #动态路由#
	tree1.AddRoute("GET", "/*", new(TRoute))                       // 泛解析
	tree1.AddRoute("GET", "/(:aa)", new(TRoute))                   // 泛解析
	tree1.AddRoute("GET", "/(:aa)/(:bb)", new(TRoute))             // 泛解析
	tree1.AddRoute("GET", "/aa/(:aa)/bb", new(TRoute))             // 中间测试
	tree1.AddRoute("GET", "/aa/(:aa)/(:bb)/bb", new(TRoute))       // 高级中间测试
	tree1.AddRoute("GET", "/aa/(:aa)/bb/(:bb)/cc", new(TRoute))    // 中级中间测试
	tree1.AddRoute("GET", "/(string:aa)-(string:bb)", new(TRoute)) // 带变量泛解析
	tree1.AddRoute("GET", "/(int:aa)-(string:bb)", new(TRoute))    // 带变量泛解析
	tree1.AddRoute("GET", "/(int:aa)-(int:bb)", new(TRoute))       // 带变量泛解析
	tree1.AddRoute("GET", "/(int:aa)-(int:bb)/(:cc)", new(TRoute)) // 带变量泛解析
	tree1.AddRoute("GET", "/aaa/(:aa)bbb(:bb)", new(TRoute))       // #多控制器测试#
	tree2.AddRoute("GET", "/aaa/(:aa)bbb(:bb)", new(TRoute))       // #多控制器测试#
	tree1.Conbine(tree2)                                           // #多控制器测试#
	tree1.PrintTrees()

	fmt.Println("Test macth")

	// :MATCH: /(:aa)"
	r, p := tree1.Match("GET", "/abcdefg~!@#$%^&*()-=_+{}:<>?|[];,./")
	if r != nil {
		if r.Path != "/(:aa)" {
			t.Logf("/(:aa) not matched! %v %v", r.Path, p)
		}
	}

	// :MATCH: /(:aa)/(:bb)
	r, p = tree1.Match("GET", "/aa/Aa.bB/abc")
	if r != nil {
		if r.Path != "/(:aa)/(:bb)" {
			t.Logf("/(:aa)/(:bb) not matched! %v %v", r.Path, p)
		}
	}
}
