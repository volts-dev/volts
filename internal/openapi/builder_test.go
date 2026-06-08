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

func TestBuildSpec_TagsAndDescription(t *testing.T) {
	eps := []*registry.Endpoint{{
		Name:        "/p",
		Path:        "/p",
		Method:      []string{"GET"},
		Description: "short summary",
		Tags:        []string{"pets"},
		Metadata:    map[string]string{"description": "the long description"},
	}}
	b := BuildSpec(Info{Title: "T", Version: "1.0.0"}, eps)

	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(b)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := doc.Validate(loader.Context); err != nil {
		t.Fatalf("validate: %v\n%s", err, b)
	}
	op := doc.Paths.Find("/p").Get
	if op.Summary != "short summary" {
		t.Fatalf("summary wrong: %q", op.Summary)
	}
	if op.Description != "the long description" {
		t.Fatalf("description not forwarded: %q", op.Description)
	}
	if len(op.Tags) != 1 || op.Tags[0] != "pets" {
		t.Fatalf("tags not forwarded: %v", op.Tags)
	}
}

func TestBuildSpec_ClassifiesParamsByInTag(t *testing.T) {
	eps := []*registry.Endpoint{{
		Name: "/items/:id", Path: "/items/:id", Method: []string{"POST"},
		Request: &registry.Value{Name: "Req", Type: "Req", Values: []*registry.Value{
			{Name: "id", Type: "string", In: "path", Required: true},
			{Name: "q", Type: "string", In: "query"},
			{Name: "body1", Type: "string"}, // no in tag => body
		}},
	}}
	b := BuildSpec(Info{Title: "T", Version: "1.0.0"}, eps)

	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(b)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := doc.Validate(loader.Context); err != nil {
		t.Fatalf("validate: %v\n%s", err, b)
	}
	item := doc.Paths.Find("/items/{id}")
	if item == nil || item.Post == nil {
		t.Fatalf("operation missing: %s", b)
	}
	op := item.Post
	var hasPath, hasQuery bool
	for _, pr := range op.Parameters {
		if pr.Value.In == "path" && pr.Value.Name == "id" {
			hasPath = true
		}
		if pr.Value.In == "query" && pr.Value.Name == "q" {
			hasQuery = true
		}
	}
	if !hasPath || !hasQuery {
		t.Fatalf("params not classified: path=%v query=%v\n%s", hasPath, hasQuery, b)
	}
	if op.RequestBody == nil {
		t.Fatalf("requestBody missing for body1\n%s", b)
	}
	bodySchema := op.RequestBody.Value.Content["application/json"].Schema.Value
	if bodySchema.Properties["body1"] == nil {
		t.Fatalf("body1 not in requestBody")
	}
	if bodySchema.Properties["q"] != nil || bodySchema.Properties["id"] != nil {
		t.Fatalf("param leaked into requestBody: %+v", bodySchema.Properties)
	}
}
