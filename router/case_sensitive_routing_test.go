package router

import (
	"net/http/httptest"
	"testing"

	"github.com/volts-dev/volts/transport"
)

// TestRouting_IsCaseSensitive 钉住一条**性质**：路由匹配区分大小写。
//
// 曾经有个 `WithIgnoreCase()`，`NewGroup` 还在给每棵路由树默认带上它 ——
// 但 `TTree.IgnoreCase` 这个字段从来没有任何读取方，匹配自始至终是区分大小写的。
// 也就是说框架声明了一个默认行为，然后忽略了它。
//
// 删掉而不是实现，理由有二：
//   - 打开它会**改变每一个既有路由的匹配范围**，包括下游已经上线的；
//     `/{module}/static/...` 这类路径还落在大小写敏感的文件系统上，两边一旦不一致
//     就成了"路由匹配上了、文件打不开"。
//   - 大小写不敏感的路径匹配是一类经典的 ACL 绕过形状：按路径前缀做鉴权的中间件
//     判的是一种大小写，后端取的是另一种。
//
// 以后谁要加回大小写不敏感匹配，必须先让这条变红。
func TestRouting_IsCaseSensitive(t *testing.T) {
	r := New()
	defer r.Close()

	grp := NewGroup()
	grp.Url([]string{"GET"}, "/Case/Sensitive", func(ctx *THttpContext) {
		ctx.Respond([]byte("hit"))
	})
	r.RegisterGroup(grp)

	for _, tc := range []struct {
		path string
		want int
	}{
		{"/Case/Sensitive", 200},
		{"/case/sensitive", 404},
		{"/CASE/SENSITIVE", 404},
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, transport.NewHttpRequest(httptest.NewRequest("GET", tc.path, nil)))
		if rec.Code != tc.want {
			t.Errorf("GET %s -> %d，应为 %d", tc.path, rec.Code, tc.want)
		}
	}
}
