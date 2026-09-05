package router

import (
	"net/http"
	"testing"
)

// TestApply_BodylessStatusesDropTheBody 钉的是：204 / 304 记下状态码之后，再
// Respond 出来的正文必须**不出去**。
//
// ★ 为什么非要在 recorder 上钉一遍：真栈上 net/http 自己就会拦（ErrBodyNotAllowed），
// 所以只跑 serveLive 的话这条永远绿，看不出 volts 这一层有没有做。而
// httptest.ResponseRecorder 不拦 —— 用 recorder 写单测的调用方（vectors 各模块都是）
// 会看见一个真栈上并不存在的 304 带正文，判据与真栈相反。两边一致才不会互相打架。
func TestApply_BodylessStatusesDropTheBody(t *testing.T) {
	cases := []struct {
		name string
		path string
		code int
	}{
		{"304 Not Modified", "/bodyless/304", http.StatusNotModified},
		{"204 No Content", "/bodyless/204", http.StatusNoContent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := serveOnce(t, "GET", c.path, func(ctx *THttpContext) {
				ctx.WriteHeader(c.code)
				ctx.Respond([]byte("这段不该出现"))
			})
			if rec.Code != c.code {
				t.Fatalf("status=%d, want %d", rec.Code, c.code)
			}
			if got := rec.Body.String(); got != "" {
				t.Fatalf("%d 带了正文 %q", c.code, got)
			}
		})
	}
}

// 反向断言：正常状态码一个字节都不能少 —— 上面那道闸不许连累别人。
func TestApply_OrdinaryStatusStillCarriesBody(t *testing.T) {
	rec := serveOnce(t, "GET", "/bodyless/200", func(ctx *THttpContext) {
		ctx.WriteHeader(http.StatusAccepted)
		ctx.Respond([]byte("hi"))
	})
	if rec.Code != 202 || rec.Body.String() != "hi" {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}
