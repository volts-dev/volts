package client

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/volts-dev/volts/codec"
)

// TestHttpResponse_ReadDecodesTheBody 锁定 Read 真的把正文解出来。
//
// 它此前的函数体就是 `return nil` —— 调用方拿到一个原封不动的零值和一个 nil 错误，
// 看起来一切正常。方法不在 IResponse 接口里、本仓也没有调用方，但 `HttpResponse`
// 是导出别名，外部拿得到它，而方法名承诺的正是"把响应读出来"。
func TestHttpResponse_ReadDecodesTheBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"name":"volts","n":7}`))
	}))
	defer ts.Close()

	cli, err := NewHttpClient()
	if err != nil {
		t.Fatal(err)
	}
	req, err := cli.NewRequest("GET", ts.URL+"/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	rsp, err := cli.Call(req)
	if err != nil {
		t.Fatal(err)
	}

	hr, ok := rsp.(*httpResponse)
	if !ok {
		t.Fatalf("响应类型是 %T", rsp)
	}

	var out struct {
		Name string `json:"name"`
		N    int    `json:"n"`
	}
	if err := hr.Read(&out); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out.Name != "volts" || out.N != 7 {
		t.Fatalf("解出来的是 %+v —— Read 没有真的解码（旧实现直接 return nil）", out)
	}
}

// TestHttpResponse_BodyIsAlreadyMaterialized 锁定正文在构造时就已读完：连着取两次
// 拿到的是同一份完整内容。
//
// 此前 Body() 会在长度为 0 时去补读 `self.response.Body`，而那个 reader 早已被
// `defer hrsp.Body.Close()` 关掉；补读那段的错误判断还是反的
// （`if err == nil { log.Err(err) }`）—— 读成功时打印一个 nil，真出错时一声不响。
func TestHttpResponse_BodyIsAlreadyMaterialized(t *testing.T) {
	const payload = `{"ok":true}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(payload))
	}))
	defer ts.Close()

	cli, err := NewHttpClient()
	if err != nil {
		t.Fatal(err)
	}
	req, err := cli.NewRequest("GET", ts.URL+"/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	rsp, err := cli.Call(req)
	if err != nil {
		t.Fatal(err)
	}

	first := string(rsp.Body().AsBytes())
	second := string(rsp.Body().AsBytes())
	if first != payload || second != payload {
		t.Fatalf("两次取到的正文是 %q / %q，期望都是 %q", first, second, payload)
	}
}

// TestHttpResponse_EmptyBodyReadReportsError 锁定空正文上的 Read 报错而不是假装成功。
func TestHttpResponse_EmptyBodyReadReportsError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	cli, err := NewHttpClient()
	if err != nil {
		t.Fatal(err)
	}
	req, err := cli.NewRequest("GET", ts.URL+"/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	rsp, err := cli.Call(req)
	if err != nil {
		t.Fatal(err)
	}
	hr := rsp.(*httpResponse)

	var out map[string]any
	if err := hr.Read(&out); err == nil {
		t.Error("空正文解码应当报错，而不是回一个 nil 让调用方以为拿到了数据")
	}
}

// TestResponseCodec_PicksByResponseContentType 锁定解码器按**响应**的 Content-Type 选，
// 并且走的是 IdentifyWireCodec（对端不能借 Content-Type 挑一个受限的解码器）。
func TestResponseCodec_PicksByResponseContentType(t *testing.T) {
	fallback := codec.IdentifyCodec(codec.Bytes)
	if fallback == nil {
		t.Skip("bytes codec 未注册")
	}

	cases := []struct {
		name        string
		contentType string
		wantJSON    bool
	}{
		{"JSON", "application/json", true},
		{"JSON 带参数", "application/json; charset=UTF-8", true},
		{"大小写与空格", "  APPLICATION/JSON  ", true},
		{"没有 Content-Type 时回落", "", false},
		{"不认识的类型回落", "application/x-nonesuch", false},
		// Gob 默认不可被对端选中（内存放大），Content-Type 说了也不算。
		{"受限解码器不给对端选", "gob", false},
	}
	for _, c := range cases {
		rsp := &http.Response{Header: http.Header{}}
		if c.contentType != "" {
			rsp.Header.Set("Content-Type", c.contentType)
		}
		got := responseCodec(rsp, fallback)
		isJSON := got == codec.IdentifyCodec(codec.JSON)
		if isJSON != c.wantJSON {
			t.Errorf("%s: 选到的解码器 isJSON=%v, want %v", c.name, isJSON, c.wantJSON)
		}
		if !c.wantJSON && got != fallback {
			t.Errorf("%s: 应回落到请求那一侧的解码器", c.name)
		}
	}

	if got := responseCodec(nil, fallback); got != fallback {
		t.Error("响应为 nil 时应回落")
	}
}
