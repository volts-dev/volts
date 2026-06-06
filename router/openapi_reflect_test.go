package router

import (
	"reflect"
	"testing"
)

type opAddr struct {
	City string `json:"city"`
}
type opUser struct {
	ID     string   `json:"id" in:"path" required:"true" format:"uuid"`
	Age    int      `json:"age"`
	Tags   []string `json:"tags"`
	Addr   opAddr   `json:"addr"`
	Parent *opUser  `json:"parent"` // self-reference must not infinite-loop
}

func findField(v *Value, name string) *Value {
	for _, f := range v.Values {
		if f.Name == name {
			return f
		}
	}
	return nil
}

func TestReflectValue_Basic(t *testing.T) {
	v := reflectValue(reflect.TypeOf(opUser{}))
	if v == nil || v.Type != "opUser" {
		t.Fatalf("want struct opUser, got %+v", v)
	}
	id := findField(v, "id")
	if id == nil || !id.Required || id.Format != "uuid" || id.In != "path" {
		t.Fatalf("id field tags wrong: %+v", id)
	}
	tags := findField(v, "tags")
	if tags == nil || tags.Type != "[]string" {
		t.Fatalf("tags slice type wrong: %+v", tags)
	}
	addr := findField(v, "addr")
	if addr == nil || len(addr.Values) != 1 {
		t.Fatalf("nested struct addr not expanded: %+v", addr)
	}
}
