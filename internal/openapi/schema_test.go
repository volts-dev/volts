package openapi

import (
	"testing"

	"github.com/volts-dev/volts/registry"
)

func TestValueToSchema_StructAndSlice(t *testing.T) {
	v := &registry.Value{
		Name: "User", Type: "User",
		Values: []*registry.Value{
			{Name: "id", Type: "string", Required: true, Format: "uuid"},
			{Name: "age", Type: "int"},
			{Name: "tags", Type: "[]string"},
		},
	}
	comps := map[string]bool{}
	s := valueToSchema(v, comps)
	if s == nil || s.Value == nil {
		t.Fatal("nil schema")
	}
	if s.Value.Type == nil || !s.Value.Type.Is("object") {
		t.Fatalf("want object, got %v", s.Value.Type)
	}
	if s.Value.Properties["id"] == nil {
		t.Fatal("id property missing")
	}
	if len(s.Value.Required) != 1 || s.Value.Required[0] != "id" {
		t.Fatalf("required wrong: %v", s.Value.Required)
	}
	if s.Value.Properties["tags"].Value.Type == nil || !s.Value.Properties["tags"].Value.Type.Is("array") {
		t.Fatal("tags should be array")
	}
}
