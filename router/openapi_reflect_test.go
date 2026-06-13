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

// opNoTag 故意不带 json tag —— 复现「无 tag 字段名塌缩成类型名」的回归用例。
type opNoTag struct {
	Name string
	Age  int
	OK   bool
}

// TestReflectValue_NoJSONTag 保证无 json tag 的字段用 Go 字段名作 Value.Name，
// 而非类型名（"string"/"int"）——后者会让 OpenAPI 请求体属性塌缩、入参消失。
func TestReflectValue_NoJSONTag(t *testing.T) {
	v := reflectValue(reflect.TypeOf(opNoTag{}))
	if v == nil || len(v.Values) != 3 {
		t.Fatalf("want 3 fields, got %+v", v)
	}
	for _, want := range []struct{ name, typ string }{
		{"Name", "string"}, {"Age", "int"}, {"OK", "bool"},
	} {
		f := findField(v, want.name)
		if f == nil {
			t.Fatalf("field %q missing (collapsed to type name?): %+v", want.name, v.Values)
		}
		if f.Type != want.typ {
			t.Fatalf("field %q type = %q, want %q", want.name, f.Type, want.typ)
		}
	}
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
