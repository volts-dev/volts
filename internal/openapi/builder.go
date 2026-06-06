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

		// Collect the set of path-param names from the converted URL (e.g. {id}).
		urlPathParams := map[string]bool{}
		for _, match := range openAPIParamRe.FindAllStringSubmatch(oapiPath, -1) {
			urlPathParams[match[1]] = true
		}

		// coveredPathParams tracks which URL path-param names were provided by a
		// request field with in:"path", so we don't double-declare them.
		coveredPathParams := map[string]bool{}

		if ep.Request != nil && len(ep.Request.Values) > 0 {
			// Object-style request: classify each field by its In tag.
			var bodyFields []*registry.Value
			for _, f := range ep.Request.Values {
				switch f.In {
				case "path":
					op.Parameters = append(op.Parameters, &openapi3.ParameterRef{
						Value: &openapi3.Parameter{
							Name:     f.Name,
							In:       "path",
							Required: true, // path params are always required
							Schema:   valueToSchema(f, comps),
						},
					})
					coveredPathParams[f.Name] = true
				case "query", "header":
					op.Parameters = append(op.Parameters, &openapi3.ParameterRef{
						Value: &openapi3.Parameter{
							Name:     f.Name,
							In:       f.In,
							Required: f.Required,
							Schema:   valueToSchema(f, comps),
						},
					})
				default:
					// No in tag, or explicit "body" — goes into requestBody.
					bodyFields = append(bodyFields, f)
				}
			}

			// Any URL path param not covered by a request field gets a default string param.
			for name := range urlPathParams {
				if !coveredPathParams[name] {
					op.Parameters = append(op.Parameters, &openapi3.ParameterRef{
						Value: &openapi3.Parameter{
							Name:     name,
							In:       "path",
							Required: true,
							Schema:   openapi3.NewStringSchema().NewRef(),
						},
					})
				}
			}

			if len(bodyFields) > 0 {
				bodyVal := &registry.Value{Name: "Body", Type: "Body", Values: bodyFields}
				body := openapi3.NewRequestBody().WithJSONSchemaRef(valueToSchema(bodyVal, comps))
				op.RequestBody = &openapi3.RequestBodyRef{Value: body}
			}
		} else {
			// No request, or scalar request (no Values): declare URL path params as
			// default string params and (if scalar request) set it as requestBody.
			for name := range urlPathParams {
				op.Parameters = append(op.Parameters, &openapi3.ParameterRef{
					Value: &openapi3.Parameter{
						Name:     name,
						In:       "path",
						Required: true,
						Schema:   openapi3.NewStringSchema().NewRef(),
					},
				})
			}
			if ep.Request != nil {
				body := openapi3.NewRequestBody().WithJSONSchemaRef(valueToSchema(ep.Request, comps))
				op.RequestBody = &openapi3.RequestBodyRef{Value: body}
			}
		}

		op.Responses = openapi3.NewResponses()
		resp := openapi3.NewResponse().WithDescription("OK")
		if ep.Response != nil {
			resp.WithJSONSchemaRef(valueToSchema(ep.Response, comps))
		}
		op.Responses.Set("200", &openapi3.ResponseRef{Value: resp})

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
