package router

import (
	"testing"

	"github.com/volts-dev/volts/registry"
)

func TestAllEndpoints_IncludesHandleRoutes(t *testing.T) {
	r := New()
	defer close(r.exit)
	grp := NewGroup()
	Handle(grp, "POST", "/hello-ae", func(ctx IContext, in *hReq) (*hRsp, error) {
		return &hRsp{}, nil
	}, OpSummary("hello"))
	r.RegisterGroup(grp)

	eps := r.GetRoutes().AllEndpoints()
	var found *registry.Endpoint
	for _, ep := range eps {
		if ep.Path == "/hello-ae" {
			found = ep
		}
	}
	if found == nil {
		t.Fatalf("/hello-ae not in AllEndpoints (got %d eps)", len(eps))
	}
	if found.Description != "hello" {
		t.Fatalf("description not propagated: %q", found.Description)
	}
}
