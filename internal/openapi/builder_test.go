package openapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/volts-dev/volts/registry"
)

func TestBuildSpec_ParsableAndHasPath(t *testing.T) {
	eps := []*registry.Endpoint{
		{
			Name:        "/users/:id",
			Path:        "/users/:id",
			Method:      []string{"GET"},
			Description: "get user",
			Response:    &registry.Value{Name: "User", Type: "User", Values: []*registry.Value{{Name: "id", Type: "string"}}},
		},
	}
	b := BuildSpec(Info{Title: "T", Version: "1.0.0"}, eps)
	if len(b) == 0 {
		t.Fatal("empty spec")
	}
	if !strings.Contains(string(b), "/users/{id}") {
		t.Fatalf("path not converted: %s", b)
	}
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(b)
	if err != nil {
		t.Fatalf("spec not loadable: %v", err)
	}
	if err := doc.Validate(loader.Context); err != nil {
		t.Fatalf("spec invalid: %v", err)
	}
	var raw map[string]any
	_ = json.Unmarshal(b, &raw)
	if raw["openapi"] == nil {
		t.Fatal("missing openapi version field")
	}
}
