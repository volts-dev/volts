package router

import "testing"

func TestMacth(t *testing.T) {
	tree := NewRouteTree()
	route := newRoute(nil, []string{"GET"}, nil, "/<:lang>/<:name>-<int:id>.do", "", "", "")
	tree.AddRoute(route)
	route = newRoute(nil, []string{"GET"}, nil, "/<:lang>/<:id>.do", "", "", "")
	tree.AddRoute(route)
	route = newRoute(nil, []string{"GET"}, nil, "/<:id>.do", "", "", "")
	tree.AddRoute(route)

	path := "/abc.do"
	r, params := tree.Match("GET", path)
	if r != nil {
		t.Logf("Match %s %v", path, params)
	}

	path = "/cn/abc.do"
	r, params = tree.Match("GET", path)
	if r != nil {
		t.Logf("Match %s %v", path, params)
	}

	path = "/cn/a-b-c-1.do"
	r, params = tree.Match("GET", path)
	if r != nil {
		t.Logf("Match %s %v", path, params)
	}
}
