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
	// id 的 format 透传
	if s.Value.Properties["id"].Value.Format != "uuid" {
		t.Fatalf("id format not propagated: %q", s.Value.Properties["id"].Value.Format)
	}
}

func TestValueToSchema_Scalars(t *testing.T) {
	cases := []struct {
		typ  string
		want string
	}{
		{"string", "string"},
		{"bool", "boolean"},
		{"int", "integer"},
		{"int64", "integer"},
		{"uint32", "integer"},
		{"float64", "number"},
	}
	for _, c := range cases {
		s := valueToSchema(&registry.Value{Name: "f", Type: c.typ}, map[string]bool{})
		if s == nil || s.Value == nil || s.Value.Type == nil || !s.Value.Type.Is(c.want) {
			t.Fatalf("type %q: want schema %q, got %+v", c.typ, c.want, s.Value.Type)
		}
	}
}

func TestValueToSchema_NilAndEnumDescription(t *testing.T) {
	if s := valueToSchema(nil, map[string]bool{}); s == nil || s.Value == nil {
		t.Fatal("nil input should yield a non-nil fallback schema")
	}
	s := valueToSchema(&registry.Value{
		Name: "status", Type: "string",
		Description: "the status", Enum: []string{"on", "off"},
	}, map[string]bool{})
	if s.Value.Description != "the status" {
		t.Fatalf("description not propagated: %q", s.Value.Description)
	}
	if len(s.Value.Enum) != 2 {
		t.Fatalf("enum not propagated: %v", s.Value.Enum)
	}
}
