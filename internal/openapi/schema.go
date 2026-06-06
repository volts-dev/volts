package openapi

import (
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/volts-dev/volts/registry"
)

// valueToSchema 把 registry.Value 树转成 openapi3 schema 引用。
// comps 记录已展开的具名结构体类型（预留 $ref 去重；本期内联展开）。
//
// 布局约定：本函数假定输入由 router.reflectValue 生成——即对 []Struct，
// v.Values 直接是元素结构体的「扁平字段」列表（reflectValue 设 v.Values =
// inner.Values）。注意 router/subscriber.go 的旧 extractValue 用的是「包一层」
// 布局（v.Values=[{元素结构体}]），与此不兼容；当前 OpenAPI builder 只消费
// reflectValue 经 route.meta 产出的契约，不走 subscriber 路径，故无冲突。
// 若未来要把 subscriber 的 Value 也喂进来，需先归一化两种布局。
func valueToSchema(v *registry.Value, comps map[string]bool) *openapi3.SchemaRef {
	if v == nil {
		return openapi3.NewStringSchema().NewRef()
	}

	// slice: "[]xxx"（v.Values 为元素结构体的扁平字段，见上方布局约定）
	if strings.HasPrefix(v.Type, "[]") {
		items := &registry.Value{Type: strings.TrimPrefix(v.Type, "[]"), Values: v.Values}
		arr := openapi3.NewArraySchema()
		arr.Items = valueToSchema(items, comps)
		return arr.NewRef()
	}

	// struct: 有子字段
	if len(v.Values) > 0 || v.Type == v.Name {
		obj := openapi3.NewObjectSchema()
		obj.Properties = openapi3.Schemas{}
		for _, f := range v.Values {
			obj.Properties[f.Name] = valueToSchema(f, comps)
			if f.Required {
				obj.Required = append(obj.Required, f.Name)
			}
		}
		return obj.NewRef()
	}

	// 标量
	var s *openapi3.Schema
	switch v.Type {
	case "string":
		s = openapi3.NewStringSchema()
	case "bool":
		s = openapi3.NewBoolSchema()
	case "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64":
		s = openapi3.NewIntegerSchema()
	case "float32", "float64":
		s = openapi3.NewFloat64Schema()
	case "object":
		s = openapi3.NewObjectSchema()
	default:
		s = openapi3.NewStringSchema()
	}
	if v.Format != "" {
		s.Format = v.Format
	}
	if v.Description != "" {
		s.Description = v.Description
	}
	for _, e := range v.Enum {
		s.Enum = append(s.Enum, e)
	}
	return s.NewRef()
}
