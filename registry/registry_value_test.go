package registry

import (
	"encoding/json"
	"testing"
)

func TestValue_NewFields_OmitemptyJSON(t *testing.T) {
	v := &Value{Name: "id", Type: "string", Required: true, Format: "uuid"}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !contains(got, `"required":true`) || !contains(got, `"format":"uuid"`) {
		t.Fatalf("expected required/format in json, got %s", got)
	}
	empty, _ := json.Marshal(&Value{Name: "x", Type: "int"})
	if contains(string(empty), "required") || contains(string(empty), "enum") {
		t.Fatalf("empty value must omit new fields, got %s", empty)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
