package volts

import (
	"github.com/volts-dev/volts/internal/openapi"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/router"
)

type openAPIConfig struct {
	enabled bool
	title   string
	version string
	gateway bool
	specURL string
	docsURL string
}

// OpenAPIOption 配置 OpenAPI。
type OpenAPIOption func(*openAPIConfig)

func OpenAPITitle(s string) OpenAPIOption   { return func(c *openAPIConfig) { c.title = s } }
func OpenAPIVersion(s string) OpenAPIOption { return func(c *openAPIConfig) { c.version = s } }
func OpenAPIGateway(b bool) OpenAPIOption   { return func(c *openAPIConfig) { c.gateway = b } }

// OpenAPI 启用 OpenAPI 文档与 Scalar UI。所有路由注册完成后（BeforeStart）
// 构建冻结 spec 并挂载 /openapi.json 与 /docs。
func OpenAPI(opts ...OpenAPIOption) Option {
	return func(cfg *Config) {
		oc := &openAPIConfig{
			enabled: true, title: "Volts API", version: "1.0.0",
			specURL: "/openapi.json", docsURL: "/docs",
		}
		for _, fn := range opts {
			fn(oc)
		}
		cfg.openAPI = oc
		cfg.BeforeStart = append(cfg.BeforeStart, func() error {
			mountOpenAPI(cfg)
			return nil
		})
	}
}

// mountOpenAPI 构建冻结 spec 并注册 /openapi.json 与 /docs 路由。
func mountOpenAPI(cfg *Config) {
	oc := cfg.openAPI
	if oc == nil || !oc.enabled || cfg.Server == nil {
		return
	}
	r := cfg.Server.Config().Router
	if r == nil {
		return
	}

	eps := routerEndpoints(r)
	if oc.gateway {
		if reg := cfg.Server.Config().Registry; reg != nil {
			if svcs, err := reg.ListServices(); err == nil {
				for _, s := range svcs {
					eps = append(eps, s.Endpoints...)
				}
			}
		}
	}

	spec := openapi.BuildSpec(openapi.Info{Title: oc.title, Version: oc.version}, eps)
	html := openapi.ScalarHTML(oc.specURL)

	grp := router.NewGroup()
	grp.Url("GET", oc.specURL, func(ctx *router.THttpContext) {
		ctx.SetHeader(true, "Content-Type", "application/json")
		ctx.Respond(spec)
	})
	grp.Url("GET", oc.docsURL, func(ctx *router.THttpContext) {
		ctx.SetHeader(true, "Content-Type", "text/html; charset=utf-8")
		ctx.Respond([]byte(html))
	})
	r.RegisterGroup(grp)
}

// routerEndpoints 通过类型断言取出全部路由端点（IRouter 接口未暴露 AllEndpoints）。
func routerEndpoints(r router.IRouter) []*registry.Endpoint {
	if g, ok := r.(interface{ GetRoutes() *router.TTree }); ok {
		return g.GetRoutes().AllEndpoints()
	}
	return nil
}
