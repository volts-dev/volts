package errors

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestCode_WalksTheChain 锁定：状态码必须能穿过包装取到。
//
// 业务代码普遍 `fmt.Errorf("...: %w", err)` 再往上抛，用类型断言取码的一遇到包装
// 就落空，上层只好一律当 500 —— 真栈上一次注册中心抖动因此在前端表现为 500 而不是
// 它本该有的码。
func TestCode_WalksTheChain(t *testing.T) {
	base := New("svc", 404, "not found")
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"裸的", base, 404},
		{"包一层", fmt.Errorf("load: %w", base), 404},
		{"包三层", fmt.Errorf("a: %w", fmt.Errorf("b: %w", fmt.Errorf("c: %w", base))), 404},
		{"Wrap 包住", Wrap(base, "outer", 503, "upstream down"), 503},
		{"不带码", fmt.Errorf("plain"), 0},
	}
	for _, c := range cases {
		if got := Code(c.err); got != c.want {
			t.Errorf("%s: Code=%d, want %d", c.name, got, c.want)
		}
	}
}

// TestWrap_KeepsCauseReachable 锁定 Wrap 之后底层原因仍可判定 —— 这正是
// client.RetryOnConnectError 依赖的能力。
func TestWrap_KeepsCauseReachable(t *testing.T) {
	sentinel := New("volts.client", 503, "connection could not be established")
	err := Wrap(sentinel, "volts.client", 500, "dial 10.0.0.1:9: refused")

	if !Is(err, sentinel) {
		t.Fatal("Wrap 之后底层哨兵判不出来了")
	}
	if got := Code(err); got != 500 {
		t.Errorf("外层码应为 500，实际 %d", got)
	}
	if e := From(err); e == nil || e.Detail != "dial 10.0.0.1:9: refused" {
		t.Errorf("From 应取到最外层的 Error，实际 %+v", e)
	}
	// detail 留空时沿用底层文案，不吞掉原来的话
	if got := Wrap(sentinel, "x", 500, "").Error(); got != sentinel.Error() {
		t.Errorf("空 detail 应沿用底层文案，实际 %q", got)
	}
}

// TestIs_SurvivesJSONRoundTrip 锁定：哨兵经 RPC 以 JSON 往返之后仍然认得出。
//
// 从前只有指针相等才算相等，而往返回来的是一个新对象 —— 判等永远为假，调用方
// 于是走进"未知错误"分支。
func TestIs_SurvivesJSONRoundTrip(t *testing.T) {
	raw, err := json.Marshal(Shutdown)
	if err != nil {
		t.Fatal(err)
	}
	revived := Parse(string(raw))
	if !Is(revived, Shutdown) {
		t.Fatalf("JSON 往返后认不出哨兵了: %+v", revived)
	}
	if Is(revived, PermissionDenied) {
		t.Fatal("不该与另一个哨兵相等")
	}
}

// TestIs_DistinguishesSentinelsThatShareIdAndCode 锁定必须比 Detail。
//
// PermissionDenied 与 UnavailableService 的 Id 同为 ""、Code 同为 500，只差 Detail
// —— 少比一个字段，两个语义完全不同的错误就成了同一个。
func TestIs_DistinguishesSentinelsThatShareIdAndCode(t *testing.T) {
	if Is(PermissionDenied, UnavailableService) {
		t.Fatal("PermissionDenied 与 UnavailableService 被判成了同一个错误")
	}
	if !Is(PermissionDenied, PermissionDenied) {
		t.Fatal("自己与自己都判不相等")
	}
}

// TestCause_IsNotSerialized 锁定原因链只在本进程内有效：它不该出现在线上报文里
// （跨进程传的仍然只有四个字段）。
func TestCause_IsNotSerialized(t *testing.T) {
	err := Wrap(New("inner", 401, "token expired"), "outer", 500, "boom")
	raw, e := json.Marshal(err)
	if e != nil {
		t.Fatal(e)
	}
	var m map[string]any
	if e := json.Unmarshal(raw, &m); e != nil {
		t.Fatal(e)
	}
	for _, k := range []string{"cause", "Cause"} {
		if _, ok := m[k]; ok {
			t.Fatalf("原因链被序列化出去了: %s", raw)
		}
	}
	if m["detail"] != "boom" || int(m["code"].(float64)) != 500 {
		t.Fatalf("四个字段应原样上线，实际 %s", raw)
	}
}

// TestDetail_HasNoStrayBrackets 锁定错误文案里不再有 Go 的切片方括号。
//
// 从前 `fmt.Sprintf("%s Forbidden %v", id, msg)` 里的 msg 是 `...any`，`%v` 打印的是
// 切片本身 —— 用户在红条上看到的是 `acl Forbidden [user 42]`，一个参数都不传时末尾
// 还挂着空的 `[]`。同时**状态词必须留下**：调用方按它匹配错误。
func TestDetail_HasNoStrayBrackets(t *testing.T) {
	cases := []struct {
		got  error
		want string
	}{
		{Unauthorized("web.login", "token expired"), "web.login Session Expired token expired"},
		{Unauthorized("web.login"), "web.login Session Expired"},
		{Forbidden("acl", "user", 42), "acl Forbidden user 42"},
		{Forbidden(""), "Forbidden"},
		{InternalServerError("orm", "boom"), "orm Internal Server Error boom"},
		{UnsupportedCodec("volts.client", "MsgPack"), "volts.client unsupported codec MsgPack"},
	}
	for _, c := range cases {
		if got := c.got.Error(); got != c.want {
			t.Errorf("Detail=%q, want %q", got, c.want)
		}
	}

	// 状态词是调用方的匹配依据，任何情况下都不能丢。
	for _, c := range []struct {
		err    error
		status string
	}{
		{Unauthorized("x"), "Session Expired"},
		{Forbidden("x"), "Forbidden"},
		{InternalServerError("x"), "Internal Server Error"},
	} {
		if !strings.Contains(c.err.Error(), c.status) {
			t.Errorf("%q 里少了状态词 %q", c.err.Error(), c.status)
		}
	}
}
