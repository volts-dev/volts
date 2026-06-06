package openapi

import (
	"strings"
	"testing"
)

func TestScalarHTML_ReferencesSpecURL(t *testing.T) {
	html := ScalarHTML("/openapi.json")
	if !strings.Contains(html, "/openapi.json") {
		t.Fatal("spec url not embedded")
	}
	if !strings.Contains(html, "<html") {
		t.Fatal("not html")
	}
	if strings.Contains(html, "__SPEC_URL__") {
		t.Fatal("placeholder not replaced")
	}
}
