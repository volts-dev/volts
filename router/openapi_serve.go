package router

import (
	"github.com/volts-dev/volts/internal/openapi"
	"github.com/volts-dev/volts/registry"
)

const (
	// DefaultOpenAPISpecPath OpenAPI spec 的 URL
	DefaultOpenAPISpecPath = "/openapi.json"
	// DefaultOpenAPIDocsPath Scalar UI 的 URL
	DefaultOpenAPIDocsPath = "/docs"
)

// OpenAPIGroup 构建一个提供 /openapi.json 与 /docs 的路由组（类比 PprofGroup）。
//
// spec 在调用时一次性从 r 已注册的全部路由构建并冻结为字节，请求期零反射、零开销。
// 因此必须在所有业务路由注册完成之后再调用（server 在 cfg.UseOpenAPI 为真时于启动期注册）。
func OpenAPIGroup(r IRouter) *TGroup {
	cfg := r.Config()

	title := cfg.OpenAPITitle
	if title == "" {
		title = "Volts API"
	}
	version := cfg.OpenAPIVersion
	if version == "" {
		version = "1.0.0"
	}

	// 枚举本地全部路由端点（IRouter 接口未暴露 AllEndpoints，经具体类型取用）。
	var eps []*registry.Endpoint
	if g, ok := r.(interface{ GetRoutes() *TTree }); ok {
		eps = g.GetRoutes().AllEndpoints()
	}
	// 可选：聚合 registry 中的远程服务端点（微服务全景文档）。
	if cfg.OpenAPIGateway && cfg.Registry != nil {
		if svcs, err := cfg.Registry.ListServices(); err == nil {
			for _, s := range svcs {
				eps = append(eps, s.Endpoints...)
			}
		}
	}

	spec := openapi.BuildSpec(openapi.Info{Title: title, Version: version}, eps)
	html := openapi.ScalarHTML(DefaultOpenAPISpecPath)

	group := NewGroup()
	group.Url("GET", DefaultOpenAPISpecPath, func(ctx *THttpContext) {
		ctx.SetHeader(true, "Content-Type", "application/json")
		ctx.Respond(spec)
	})
	group.Url("GET", DefaultOpenAPIDocsPath, func(ctx *THttpContext) {
		ctx.SetHeader(true, "Content-Type", "text/html; charset=utf-8")
		ctx.Respond([]byte(html))
	})
	return group
}
