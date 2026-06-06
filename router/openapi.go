package router

import (
	"reflect"
	"strings"

	"github.com/volts-dev/volts/registry"
)

// Value 是 registry.Value 的别名，便于 router 内部与测试引用。
type Value = registry.Value

// reflectValue 把 Go 类型反射成 registry.Value schema 树（注册期冷路径）。
// 承接 subscriber.go extractValue，并补充 OpenAPI 所需的 tag 与防环处理。
func reflectValue(t reflect.Type) *registry.Value {
	return reflectValueVisited(t, map[reflect.Type]bool{})
}

func reflectValueVisited(t reflect.Type, seen map[reflect.Type]bool) *registry.Value {
	if t == nil {
		return nil
	}
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	v := &registry.Value{Name: t.Name(), Type: t.Name()}

	switch t.Kind() {
	case reflect.Struct:
		if seen[t] {
			// 自引用：只放类型名，不再展开，builder 据此生成 $ref
			return &registry.Value{Name: t.Name(), Type: t.Name()}
		}
		seen[t] = true
		defer delete(seen, t)
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			if sf.PkgPath != "" {
				continue // 未导出字段跳过
			}
			fv := reflectValueVisited(sf.Type, seen)
			if fv == nil {
				continue
			}
			applyTags(sf, fv)
			v.Values = append(v.Values, fv)
		}
	case reflect.Slice, reflect.Array:
		el := t.Elem()
		for el.Kind() == reflect.Ptr {
			el = el.Elem()
		}
		v.Type = "[]" + el.Name()
		if el.Kind() == reflect.Struct {
			if inner := reflectValueVisited(t.Elem(), seen); inner != nil {
				v.Values = inner.Values
			}
		}
	case reflect.Map:
		v.Type = "object" // additionalProperties，builder 处理
		if inner := reflectValueVisited(t.Elem(), seen); inner != nil {
			v.Values = inner.Values
		}
	default:
		v.Type = t.Kind().String()
	}
	return v
}

// Operation 是一个接口的 OpenAPI 元信息，挂在 route.meta 上。
type Operation struct {
	Summary     string
	Description string
	Tags        []string
	Request     *registry.Value
	Response    *registry.Value
}

// OpOption 配置 Operation。
type OpOption func(*Operation)

func OpSummary(s string) OpOption     { return func(o *Operation) { o.Summary = s } }
func OpDescription(s string) OpOption { return func(o *Operation) { o.Description = s } }
func OpTags(tags ...string) OpOption  { return func(o *Operation) { o.Tags = append(o.Tags, tags...) } }

// buildOp 反射 I/O 类型并应用选项（注册期一次）。
func buildOp[I, O any](opts ...OpOption) *Operation {
	var i I
	var o O
	op := &Operation{
		Request:  reflectValue(reflect.TypeOf(i)),
		Response: reflectValue(reflect.TypeOf(o)),
	}
	for _, fn := range opts {
		fn(op)
	}
	return op
}

// applyTags 把结构体字段的 tag 写进字段级 Value。
func applyTags(sf reflect.StructField, fv *registry.Value) {
	if j := sf.Tag.Get("json"); j != "" {
		if name := strings.Split(j, ",")[0]; name != "" && name != "-" {
			fv.Name = name
		}
	}
	if fv.Name == "" {
		fv.Name = sf.Name
	}
	if sf.Tag.Get("required") == "true" {
		fv.Required = true
	}
	fv.Format = sf.Tag.Get("format")
	fv.Description = sf.Tag.Get("description")
	fv.In = sf.Tag.Get("in")
	if e := sf.Tag.Get("enum"); e != "" {
		fv.Enum = strings.Split(e, ",")
	}
}
