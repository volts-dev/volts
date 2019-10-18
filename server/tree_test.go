package server

import (
	"fmt"
	"testing"
)

// TODO　更多更直白的测试结果
func TestTree(t *testing.T) {
	tree1 := NewRouteTree()
	tree2 := NewRouteTree()

	// #静态路由#
	tree1.AddRoute("GET", "/aaa", new(TRoute))
	tree2.AddRoute("GET", "/aaa/bbb", new(TRoute))

	////tree1.AddRoute("GET", "/abc1/tree1", r)
	tree2.AddRoute("GET", "/aaa/bbb", new(TRoute))

	// #动态路由#
	tree1.AddRoute("GET", "/*", new(TRoute))              // 泛解析
	tree1.AddRoute("GET", "/(:xx)", new(TRoute))          // 泛解析
	tree1.AddRoute("GET", "/(:xx)/(:yy)", new(TRoute))    // 泛解析
	tree1.AddRoute("GET", "/(:xx)/(:yy)/bb", new(TRoute)) // 泛解析
	tree1.AddRoute("GET", "/(:xx)-(:yy)", new(TRoute))    // 泛解析

	tree1.AddRoute("GET", "/aa/(:xx)/bb", new(TRoute))             // 中间测试
	tree1.AddRoute("GET", "/aa/(:xx)/(:yy)/bb", new(TRoute))       // 高级中间测试
	tree1.AddRoute("GET", "/aa/(:xx)/bb/(:yy)/cc", new(TRoute))    // 中级中间测试
	tree1.AddRoute("GET", "/(string:xx)-(string:yy)", new(TRoute)) // 带变量泛解析
	tree1.AddRoute("GET", "/(int:xx)-(string:yy)", new(TRoute))    // 带变量泛解析
	tree1.AddRoute("GET", "/(int:xx)-(int:yy)", new(TRoute))       // 带变量泛解析
	tree1.AddRoute("GET", "/(int:xx)-(int:yy)/(:cc)", new(TRoute)) // 带变量泛解析
	tree1.AddRoute("GET", "/aaa/(:xx)bbb(:yy)", new(TRoute))       // #多控制器测试#
	tree2.AddRoute("GET", "/aaa/(:xx)bbb(:yy)", new(TRoute))       // #多控制器测试#
	tree1.Conbine(tree2)                                           // #多控制器测试#
	tree1.PrintTrees()

	fmt.Println("Test macth")

	// :MATCH: /(:xx)"
	r, p := tree1.Match("GET", "/abcdefg~!@#$%^&*()-=_+{}:<>?|[];,.")
	if r != nil {
		if r.Path != "/(:xx)-(:yy)" {
			t.Logf("/(:xx) not matched! %v %v", r.Path, p)
		}
	} else {
		t.Log("/abcdefg~!@#$%^&*()-=_+{}:<>?|[];,. not matched!")
	}

	// :MATCH: /(:xx)/(:yy)
	r, p = tree1.Match("GET", "/aa/Aa.bB/bb")
	if r != nil {
		if r.Path != "/(:xx)/(:yy)" {
			t.Logf("/(:xx)/(:yy) not matched! %v %v", r.Path, p)
		}
	} else {
		t.Logf("/aa/Aa.bB/abb not matched!")
	}

	// :MATCH: /aaa/(:xx)bbb(:yy)
	r, p = tree1.Match("GET", "/aaa/Aa.bbbbBbb")
	if r != nil {
		if r.Path != "/aaa/(:xx)bbb(:yy)" {
			t.Logf("/aaa/Aa.bbbbBbb not matched! %v %v", r.Path, p)
		}
	} else {
		t.Logf("/aaa/Aa.bbbbBbb not matched!")
	}
}
