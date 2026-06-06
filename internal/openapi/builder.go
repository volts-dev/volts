package openapi

import (
	"regexp"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/volts-dev/volts/registry"
)

// openAPIParamRe 匹配已转换后的 "{paramName}" 形式。
var openAPIParamRe = regexp.MustCompile(`\{([A-Za-z0-9_]+)\}`)

// Info 是 spec 的标题/版本信息。
type Info struct {
	Title       string
	Version     string
	Description string
}

var pathParamRe = regexp.MustCompile(`:([A-Za-z0-9_]+)`)

// toOpenAPIPath 把 volts 的 ":id" 转成 OpenAPI 的 "{id}"。
func toOpenAPIPath(p string) string {
	return pathParamRe.ReplaceAllString(p, `{$1}`)
}

// BuildSpec 把端点列表组装成 OpenAPI 3 文档并序列化为字节（启动期一次，结果冻结缓存）。
func BuildSpec(info Info, eps []*registry.Endpoint) []byte {
	doc := &openapi3.T{
		OpenAPI: "3.0.3",
		Info: &openapi3.Info{
			Title:       info.Title,
			Version:     info.Version,
			Description: info.Description,
		},
	}
	doc.Paths = openapi3.NewPaths()
	comps := map[string]bool{}

	for _, ep := range eps {
		if ep == nil || ep.Path == "" {
			continue
		}
		oapiPath := toOpenAPIPath(ep.Path)
		item := doc.Paths.Find(oapiPath)
		if item == nil {
			item = &openapi3.PathItem{}
			doc.Paths.Set(oapiPath, item)
		}

		op := openapi3.NewOperation()
		op.Summary = ep.Description

		// 从路径中提取路径参数并声明到 operation.Parameters
		for _, match := range openAPIParamRe.FindAllStringSubmatch(oapiPath, -1) {
			paramName := match[1]
			op.Parameters = append(op.Parameters, &openapi3.ParameterRef{
				Value: &openapi3.Parameter{
					Name:     paramName,
					In:       "path",
					Required: true,
					Schema:   openapi3.NewStringSchema().NewRef(),
				},
			})
		}

		op.Responses = openapi3.NewResponses()
		resp := openapi3.NewResponse().WithDescription("OK")
		if ep.Response != nil {
			resp.WithJSONSchemaRef(valueToSchema(ep.Response, comps))
		}
		op.Responses.Set("200", &openapi3.ResponseRef{Value: resp})

		if ep.Request != nil {
			body := openapi3.NewRequestBody().WithJSONSchemaRef(valueToSchema(ep.Request, comps))
			op.RequestBody = &openapi3.RequestBodyRef{Value: body}
		}

		methods := ep.Method
		if len(methods) == 0 {
			methods = []string{"GET"}
		}
		for _, m := range methods {
			switch m {
			case "GET":
				item.Get = op
			case "POST", "CONNECT":
				item.Post = op
			case "PUT":
				item.Put = op
			case "DELETE":
				item.Delete = op
			case "PATCH":
				item.Patch = op
			}
		}
	}

	b, err := doc.MarshalJSON()
	if err != nil {
		return []byte(`{"openapi":"3.0.3","info":{"title":"error","version":"0"},"paths":{}}`)
	}
	return b
}
