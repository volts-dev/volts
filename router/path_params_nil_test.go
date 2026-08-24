package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/volts-dev/volts/transport"
)

// 没有 `{name}` 占位符的路由上，ctx.PathParams() 也不能是 nil。
//
// 失败方式：setPathParams 只在 `len(p) > 0` 时才建这个集合，而 reset 每轮把它
// 置回 nil。于是同一个池化 context 先服务一条带占位符的路由、再服务一条不带的，
// handler 里那句 `ctx.PathParams().FieldByName("x")` 就是空指针崩溃 ——
// 栈里只看得见 handler 那一行，看不出「这条路由没有路径参数」才是原因。
//
// 尤其阴的是**它跟请求顺序有关**：新建的 context 在构造函数里已经有一个空集合，
// 所以进程刚起来时同一条路由好好的，跑一阵子拿到复用的 context 才开始 500。
// 下面第二次请求走的正是复用那条路。
//
// 真实案例（2026-08-25 mass_mailing 真栈）：退订确认页渲染正常，一点
// 「Unsubscribe」提交到 `POST /mailing/confirm_unsubscribe`（无占位符）稳定 500，
// 而同一个 handler 服务的 `GET /mailing/{mailing_id}/unsubscribe` 一切正常。
func TestPathParams_NeverNilOnRouteWithoutParams(t *testing.T) {
	var got []string
	rt := New()
	defer close(rt.exit)
	grp := NewGroup()
	// 带占位符的那条：先跑它，让 context 里留下一个 pathParams。
	grp.Url([]string{"GET"}, "/with/{id}", func(ctx *THttpContext) {
		if ps := ctx.PathParams(); ps == nil {
			t.Error("带占位符的路由上 PathParams() 不该是 nil")
		} else {
			got = append(got, ps.FieldByName("id").AsString())
		}
		ctx.Respond([]byte("ok"))
	})
	// 不带占位符的那条：崩就崩在这里。
	grp.Url([]string{"GET"}, "/without", func(ctx *THttpContext) {
		ps := ctx.PathParams()
		if ps == nil {
			t.Fatal("PathParams() 返回了 nil —— handler 里任何 FieldByName 都会空指针崩溃")
		}
		f := ps.FieldByName("id")
		if f == nil {
			t.Fatal("空参数集的 FieldByName 返回 nil")
		}
		if v := f.AsString(); v != "" {
			t.Errorf("不存在的参数应取到零值，得到 %q", v)
		}
		got = append(got, "without:"+f.AsString())
		ctx.Respond([]byte("ok"))
	})

	rt.RegisterGroup(grp)

	for _, path := range []string{"/with/42", "/without", "/with/7", "/without"} {
		w := httptest.NewRecorder()
		rt.ServeHTTP(w, transport.NewHttpRequest(httptest.NewRequest(http.MethodGet, path, nil)))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: 状态码 %d，正文 %s", path, w.Code, w.Body.String())
		}
	}

	want := []string{"42", "without:", "7", "without:"}
	if len(got) != len(want) {
		t.Fatalf("handler 执行次数 %d，应为 %d（%v）", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 次: 得到 %q，应为 %q", i, got[i], want[i])
		}
	}
}
