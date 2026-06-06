package router

import "testing"

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

func TestRouteClone_CopiesMeta(t *testing.T) {
	r := newRoute(nil, []string{"GET"}, nil, "/x", "", "", "")
	r.meta = &Operation{Summary: "s"}
	c := r.Clone().(*route)
	if c.meta == nil {
		t.Fatal("clone lost meta")
	}
}
