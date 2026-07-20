package router

import (
	"net/http/httptest"
	"testing"

	"github.com/volts-dev/volts/transport"
)

// 路由级中间件：Url(methods, path, 主控制器, 中间件...) —— handlers[0] 是主控制器，
// handlers[1:] 才是中间件，且中间件先于主控制器执行。
//
// 这条断言存在的原因：参数顺序颠倒（把中间件放在 handlers[0]）不会报"参数用错"，
// 而是让中间件被当成控制器走进反射兜底路径，报一句无关的
// "reflect: call of reflect.Value.NumField on interface Value"，极易被误判成
// "这个中间件类型不被支持"。
func TestRouteLevelMiddlewareOrder(t *testing.T) {
	order := ""

	r := New()
	defer r.Close()

	// handlers[0]=主控制器，handlers[1:]=中间件
	r.Url([]string{"GET"}, "/probe",
		func(ctx *THttpContext) { order += "main" },
		func(ctx IContext) { order += "A>"; ctx.Next() },      // func(IContext)
		func(ctx *THttpContext) { order += "B>"; ctx.Next() }, // func(*THttpContext)
	)

	req := httptest.NewRequest("GET", "/probe", nil)
	r.ServeHTTP(httptest.NewRecorder(), transport.NewHttpRequest(req))

	if order != "A>B>main" {
		t.Fatalf("执行顺序 = %q, want %q", order, "A>B>main")
	}
}
