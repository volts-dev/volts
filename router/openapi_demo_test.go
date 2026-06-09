package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/volts-dev/volts/internal/openapi"
	"github.com/volts-dev/volts/transport"
)

// 本文件是一个贴近真实用法的 demo：注册一个「在团队下创建用户」的 API，
// 同时验证两件事：
//  1. 请求往返——path / query / body 三种来源的参数都被正确绑定到入参；
//  2. 文档生成——由该路由构建出的 OpenAPI 文档结构正确（可被 openapi3 解析、校验，
//     且 path/query 参数、requestBody、response schema 各归其位）。
//
// demoCreateUserReq 用 in tag 演示三种参数来源：
//   - team_id 来自 URL path（in:"path"）
//   - notify  来自 query（in:"query"）
//   - 其余字段（name/age/roles）无 in tag，归入 JSON body
type demoCreateUserReq struct {
	TeamID string   `json:"team_id" in:"path" required:"true" description:"所属团队 ID" format:"uuid"`
	Notify bool     `json:"notify" in:"query" description:"是否发送欢迎邮件"`
	Name   string   `json:"name" required:"true" description:"用户名"`
	Age    int      `json:"age" description:"年龄"`
	Roles  []string `json:"roles" description:"角色列表"`
}

type demoUser struct {
	ID     string   `json:"id"`
	TeamID string   `json:"team_id"`
	Name   string   `json:"name"`
	Age    int      `json:"age"`
	Roles  []string `json:"roles"`
	Notify bool     `json:"notify"`
}

type demoCreateUserRsp struct {
	User demoUser `json:"user"`
	Ok   bool     `json:"ok"`
}

// registerDemoUserAPI 注册 demo 路由，供两个测试复用。
func registerDemoUserAPI(r *TRouter) {
	grp := NewGroup()
	grp.Api[*THttpContext, demoCreateUserReq, demoCreateUserRsp](
		"POST", "/teams/{team_id}/users",
		func(c *THttpContext, in *demoCreateUserReq) (*demoCreateUserRsp, error) {
			// c 为具体 *THttpContext，可用其完整方法（此处仅演示可达）
			_ = c.Response()
			return &demoCreateUserRsp{
				Ok: true,
				User: demoUser{
					ID:     "u-1",
					TeamID: in.TeamID,
					Name:   in.Name,
					Age:    in.Age,
					Roles:  in.Roles,
					Notify: in.Notify,
				},
			}, nil
		},
		OpSummary("create user"),
		OpDescription("Create a user under the given team."),
		OpTags("users"),
	)
	r.RegisterGroup(grp)
}

// TestApiDemo_HTTPRoundTrip 验证 path / query / body 三种来源的参数都进入 handler 入参，
// 且响应被正确序列化回 JSON。
func TestApiDemo_HTTPRoundTrip(t *testing.T) {
	r := New()
	defer close(r.exit)
	registerDemoUserAPI(r)

	// team_id 走 path；notify 走 query；name/age/roles 走 body。
	body := `{"name":"alice","age":30,"roles":["admin","dev"]}`
	req := httptest.NewRequest("POST", "/teams/team-42/users?notify=true", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, transport.NewHttpRequest(req))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var rsp demoCreateUserRsp
	if err := json.Unmarshal(w.Body.Bytes(), &rsp); err != nil {
		t.Fatalf("response not valid json: %v\n%s", err, w.Body.String())
	}
	if !rsp.Ok {
		t.Fatalf("ok not true: %s", w.Body.String())
	}
	if rsp.User.TeamID != "team-42" {
		t.Fatalf("path param team_id not bound: got %q", rsp.User.TeamID)
	}
	if !rsp.User.Notify {
		t.Fatalf("query param notify not bound: %s", w.Body.String())
	}
	if rsp.User.Name != "alice" || rsp.User.Age != 30 {
		t.Fatalf("body fields not bound: %+v", rsp.User)
	}
	if len(rsp.User.Roles) != 2 || rsp.User.Roles[0] != "admin" {
		t.Fatalf("body slice not bound: %+v", rsp.User.Roles)
	}
}

// TestApiDemo_OpenAPISpecCorrect 验证由 demo 路由生成的 OpenAPI 文档正确：
// 可被 openapi3 解析并通过校验，路径已转换，参数 / requestBody / response 各归其位。
//
// 这里直接走文档生成管线（AllEndpoints → BuildSpec），而非注册 OpenAPIGroup 再经
// ServeHTTP 取回——后者服务 /openapi.json 的匿名闭包会被全局 handler 缓存按
// (path+funcName) 去重，导致同进程内多个 router 的 OpenAPIGroup 共用第一个的冻结
// spec（见 handler.go generateHandlerId）。OpenAPIGroup 的 HTTP 服务路径已由
// TestOpenAPIGroup_ServesSpecAndDocs 覆盖；本测试聚焦文档内容正确性。
func TestApiDemo_OpenAPISpecCorrect(t *testing.T) {
	r := New()
	defer close(r.exit)
	registerDemoUserAPI(r)

	// 与 OpenAPIGroup 相同的构建管线：枚举已注册端点 → 组装 OpenAPI 文档。
	eps := r.GetRoutes().AllEndpoints()
	specBytes := openapi.BuildSpec(openapi.Info{Title: "Demo API", Version: "2.1.0"}, eps)

	// 文档必须可被 openapi3 加载并通过校验。
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(specBytes)
	if err != nil {
		t.Fatalf("spec not loadable: %v\n%s", err, specBytes)
	}
	if err := doc.Validate(loader.Context); err != nil {
		t.Fatalf("spec invalid: %v\n%s", err, specBytes)
	}

	// info 标题 / 版本应来自 Config。
	if doc.Info.Title != "Demo API" || doc.Info.Version != "2.1.0" {
		t.Fatalf("info wrong: %q %q", doc.Info.Title, doc.Info.Version)
	}

	// 路径中的 :team_id 应转换成 {team_id}。
	item := doc.Paths.Find("/teams/{team_id}/users")
	if item == nil || item.Post == nil {
		t.Fatalf("POST /teams/{team_id}/users missing in spec:\n%s", specBytes)
	}
	op := item.Post

	// operation 元信息。
	if op.Summary != "create user" {
		t.Fatalf("summary wrong: %q", op.Summary)
	}
	if op.Description != "Create a user under the given team." {
		t.Fatalf("description wrong: %q", op.Description)
	}
	if len(op.Tags) != 1 || op.Tags[0] != "users" {
		t.Fatalf("tags wrong: %v", op.Tags)
	}

	// 参数分类：team_id 应在 path，notify 应在 query；二者都不应泄漏进 body。
	var pathParam, queryParam *openapi3.Parameter
	for _, pr := range op.Parameters {
		switch {
		case pr.Value.In == "path" && pr.Value.Name == "team_id":
			pathParam = pr.Value
		case pr.Value.In == "query" && pr.Value.Name == "notify":
			queryParam = pr.Value
		}
	}
	if pathParam == nil {
		t.Fatalf("path param team_id missing:\n%s", specBytes)
	}
	if !pathParam.Required {
		t.Fatalf("path param team_id must be required")
	}
	if queryParam == nil {
		t.Fatalf("query param notify missing:\n%s", specBytes)
	}

	// requestBody 应只含 body 字段（name/age/roles），不含 path/query 参数。
	if op.RequestBody == nil || op.RequestBody.Value == nil {
		t.Fatalf("requestBody missing:\n%s", specBytes)
	}
	bodySchema := op.RequestBody.Value.Content["application/json"].Schema.Value
	for _, want := range []string{"name", "age", "roles"} {
		if bodySchema.Properties[want] == nil {
			t.Fatalf("body field %q missing from requestBody: %+v", want, bodySchema.Properties)
		}
	}
	if bodySchema.Properties["team_id"] != nil || bodySchema.Properties["notify"] != nil {
		t.Fatalf("path/query param leaked into requestBody: %+v", bodySchema.Properties)
	}

	// 200 响应应带 schema，且包含嵌套的 user 对象与 ok 字段。
	resp := op.Responses.Value("200")
	if resp == nil || resp.Value == nil {
		t.Fatalf("200 response missing:\n%s", specBytes)
	}
	respSchema := resp.Value.Content["application/json"].Schema.Value
	if respSchema == nil || respSchema.Properties["ok"] == nil {
		t.Fatalf("response schema missing ok field: %+v", respSchema)
	}
	if respSchema.Properties["user"] == nil {
		t.Fatalf("response schema missing nested user object: %+v", respSchema.Properties)
	}
}
