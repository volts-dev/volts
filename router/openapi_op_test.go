package router

import "testing"

type opReq struct {
	Name string `json:"name" required:"true"`
}
type opRsp struct {
	OK bool `json:"ok"`
}

func TestBuildOp(t *testing.T) {
	op := buildOp[opReq, opRsp](OpSummary("create"), OpTags("user"))
	if op.Summary != "create" {
		t.Fatalf("summary not set: %q", op.Summary)
	}
	if len(op.Tags) != 1 || op.Tags[0] != "user" {
		t.Fatalf("tags wrong: %v", op.Tags)
	}
	if op.Request == nil || op.Request.Type != "opReq" {
		t.Fatalf("request schema wrong: %+v", op.Request)
	}
	if op.Response == nil || op.Response.Type != "opRsp" {
		t.Fatalf("response schema wrong: %+v", op.Response)
	}
}
