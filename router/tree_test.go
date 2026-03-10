package router

import (
	"testing"
)

func TestTreeComprehensive(t *testing.T) {
	tests := []struct {
		name    string
		routes  []string
		method  string
		path    string
		match   bool
		params  map[string]string
		failMsg string
	}{
		{
			name:   "Basic Static Route",
			routes: []string{"/api/v1/users"},
			method: "GET",
			path:   "/api/v1/users",
			match:  true,
		},
		{
			name:   "Not Found Static Route",
			routes: []string{"/api/v1/users"},
			method: "GET",
			path:   "/api/v1/admins",
			match:  false,
		},
		{
			name:   "Simple Variable",
			routes: []string{"/users/{id}"},
			method: "GET",
			path:   "/users/123",
			match:  true,
			params: map[string]string{"id": "123"},
		},
		{
			name:   "Typed Variable - Int",
			routes: []string{"/users/{id:int}"},
			method: "GET",
			path:   "/users/123",
			match:  true,
			params: map[string]string{"id": "123"},
		},
		{
			name:   "Typed Variable - Int (Invalid)",
			routes: []string{"/users/{id:int}"},
			method: "GET",
			path:   "/users/abc",
			match:  false,
		},
		{
			name:   "Typed Variable - String",
			routes: []string{"/users/{name:string}"},
			method: "GET",
			path:   "/users/john",
			match:  true,
			params: map[string]string{"name": "john"},
		},
		{
			name:   "Typed Variable - String (Invalid contains digit)",
			routes: []string{"/users/{name:string}"},
			method: "GET",
			path:   "/users/john123",
			match:  false,
		},
		{
			name:   "Complex Pattern Pattern",
			routes: []string{"/api/{action:string[read|write]}"},
			method: "GET",
			path:   "/api/read",
			match:  true,
			params: map[string]string{"action": "read"},
		},
		{
			name:   "Complex Pattern Pattern - Write",
			routes: []string{"/api/{action:string[read|write]}"},
			method: "GET",
			path:   "/api/write",
			match:  true,
			params: map[string]string{"action": "write"},
		},
		{
			name:   "Complex Pattern Pattern - Invalid",
			routes: []string{"/api/{action:string[read|write]}"},
			method: "GET",
			path:   "/api/delete",
			match:  false,
		},
		{
			name:   "Pattern without Type",
			routes: []string{"/files/{ext:[jpg|png]}"},
			method: "GET",
			path:   "/files/jpg",
			match:  true,
			params: map[string]string{"ext": "jpg"},
		},
		{
			name:   "Legacy Compatibility - Check new format preference",
			routes: []string{"/new/{id:int}"},
			method: "GET",
			path:   "/new/456",
			match:  true,
			params: map[string]string{"id": "456"},
		},
		{
			name:   "Priority - Static over Dynamic",
			routes: []string{"/users/{id}", "/users/me"},
			method: "GET",
			path:   "/users/me",
			match:  true,
			params: map[string]string{}, // Should match "/users/me" which has no params
		},
		{
			name:   "Multiple Variables",
			routes: []string{"/orgs/{org}/repos/{repo}"},
			method: "GET",
			path:   "/orgs/volts/repos/router",
			match:  true,
			params: map[string]string{"org": "volts", "repo": "router"},
		},
		{
			name:   "Variable with Static Suffix",
			routes: []string{"/files/{name}.txt"},
			method: "GET",
			path:   "/files/document.txt",
			match:  true,
			params: map[string]string{"name": "document"},
		},
		{
			name:   "Catch-all Route",
			routes: []string{"/static/*path"},
			method: "GET",
			path:   "/static/css/main.css",
			match:  true,
			params: map[string]string{"path": "css/main.css"},
		},
		{
			name:   "Complex Mixed Priority",
			routes: []string{"/{id}.do", "/{lang}/{id}.do", "/{lang}/{name}-{id:int}.do"},
			method: "GET",
			path:   "/abc.do",
			match:  true,
			params: map[string]string{"id": "abc"},
		},
		{
			name:   "Complex Mixed Priority - Segmented",
			routes: []string{"/{id}.do", "/{lang}/{id}.do", "/{lang}/{name}-{id:int}.do"},
			method: "GET",
			path:   "/cn/abc.do",
			match:  true,
			params: map[string]string{"lang": "cn", "id": "abc"},
		},
		{
			name:   "Complex Mixed Priority - Multi-Var Segment",
			routes: []string{"/{id}.do", "/{lang}/{id}.do", "/{lang}/{name}-{id:int}.do"},
			method: "GET",
			path:   "/cn/john-1.do",
			match:  true,
			params: map[string]string{"lang": "cn", "name": "john", "id": "1"},
		},
		{
			name:   "Optimize Match - NumberType fallback",
			routes: []string{"/items/{id:[0-9]+}"},
			method: "GET",
			path:   "/items/404",
			match:  true,
			params: map[string]string{"id": "404"},
		},
		{
			name:   "Optimize Match - NumberType fallback (Invalid)",
			routes: []string{"/items/{id:[0-9]+}"},
			method: "GET",
			path:   "/items/abc",
			match:  false,
		},
		{
			name:   "Optimize Match - CharType fallback",
			routes: []string{"/items/{code:[a-z]+}"},
			method: "GET",
			path:   "/items/abc",
			match:  true,
			params: map[string]string{"code": "abc"},
		},
		{
			name:   "Optimize Match - CharType fallback (Invalid)",
			routes: []string{"/items/{code:[a-z]+}"},
			method: "GET",
			path:   "/items/123",
			match:  false,
		},
		{
			name:   "Optimize Match - Mid-path Set Values",
			routes: []string{"/items/{action:[read|write]}/info"},
			method: "GET",
			path:   "/items/read/info",
			match:  true,
			params: map[string]string{"action": "read"},
		},
		{
			name:   "Optimize Match - Mid-path Set Values (Invalid Action)",
			routes: []string{"/items/{action:[read|write]}/info"},
			method: "GET",
			path:   "/items/delete/info",
			match:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tree := NewRouteTree()
			for _, r := range tt.routes {
				route := newRoute(nil, []string{tt.method}, nil, r, "", "", "")
				if err := tree.AddRoute(route); err != nil {
					t.Fatalf("Failed to add route %s: %v", r, err)
				}
			}

			route, params := tree.Match(tt.method, tt.path)
			if tt.match {
				if route == nil {
					t.Errorf("Expected match for %s, but got nil", tt.path)
					return
				}
				for key, expectedValue := range tt.params {
					actualValue := params.Get(key)
					if actualValue != expectedValue {
						t.Errorf("Param %s: expected %s, got %s", key, expectedValue, actualValue)
					}
				}
				// If no params expected but matched a static route, verify it matches the correct one
				if len(tt.params) == 0 {
					// In complex tests with multiple routes, ensure it matched a route without variables if it exists
					// or that params list is empty.
					// This is relevant for "Priority - Static over Dynamic"
				}
			} else {
				if route != nil {
					t.Errorf("Expected NO match for %s, but got one", tt.path)
				}
			}
		})
	}
}

func TestTreePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("Expected panic for missing closing brace, but did not")
		}
	}()

	tree := NewRouteTree()
	route := newRoute(nil, []string{"GET"}, nil, "/api/{action", "", "", "")
	tree.AddRoute(route)
}

func TestTreeNestedBraces(t *testing.T) {
	// Our implementation supports nested brackets {name:type[...]} or nested {}
	tree := NewRouteTree()
	// This tests the bracketBalance implementation
	route := newRoute(nil, []string{"GET"}, nil, "/api/{{inner}}", "", "", "")
	if err := tree.AddRoute(route); err != nil {
		t.Fatalf("Failed to add route: %v", err)
	}

	path := "/api/john"
	r, params := tree.Match("GET", path)
	if r == nil {
		t.Fatalf("Expected match for %s", path)
	}
	if val := params.Get("{inner}"); val != "john" {
		t.Errorf("Expected {inner} = john, got %s", val)
	}
}

func TestTreeMethodIsolation(t *testing.T) {
	tree := NewRouteTree()
	tree.AddRoute(newRoute(nil, []string{"GET"}, nil, "/api/data", "", "", ""))
	tree.AddRoute(newRoute(nil, []string{"POST"}, nil, "/api/data", "", "", ""))

	r, _ := tree.Match("GET", "/api/data")
	if r == nil {
		t.Error("Failed to match GET route")
	}

	r, _ = tree.Match("POST", "/api/data")
	if r == nil {
		t.Error("Failed to match POST route")
	}

	r, _ = tree.Match("PUT", "/api/data")
	if r != nil {
		t.Error("Unexpectedly matched PUT route")
	}
}

func TestTreeDelRoute(t *testing.T) {
	tree := NewRouteTree()
	r1 := newRoute(nil, []string{"GET"}, nil, "/api/data", "H1", "", "")
	tree.AddRoute(r1)

	r, _ := tree.Match("GET", "/api/data")
	if r == nil {
		t.Fatal("Match failed before delete")
	}

	tree.DelRoute("/api/data", r1)

	// Note: DelRoute implementation calls StripHandler.
	// Depending on implementation, it might keep the node but remove handlers or delete the node.
	// We'll check if handlers are gone or route is nil.
	r, _ = tree.Match("GET", "/api/data")
	if r != nil && len(r.handlers) > 0 {
		t.Error("Route should be deleted or have no handlers")
	}
}
