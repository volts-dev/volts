package router

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/registry/memory"
)

// 整链回归:网关收到 /fr/gw/shop，上游必须也收到 /fr/gw/shop。
//
// 只测纯函数不够——真正出问题的是**接线**:前缀记在 ctx.PathPrefix() 上，而 rewriteFn 是在
// SetURL 之后才有机会改 Out.URL。少调一次 restorePrefix，纯函数测试照样全绿，而真机上
// 语言在网关处被吃掉（实测:经网关请求 /fr 拿回英文页）。
func TestHttpReverseProxy_ForwardsStrippedLangPrefix(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte("upstream ok"))
	}))
	defer upstream.Close()

	up, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	reg := memory.New()
	if err := reg.Register(&registry.Service{
		Name: "gwsvc",
		Nodes: []*registry.Node{{
			Id:       "n1",
			Address:  up.Host,
			Metadata: map[string]string{"protocol": "http"},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	r := New(WithPathRewriter(langRewriter("fr")), WithRegistry(reg))
	defer r.Close()

	grp := NewGroup(WithAloneService(true, "gwsvc"))
	grp.Url([]string{"GET"}, "/shop", HttpReverseProxy)
	r.RegisterGroup(grp)

	w := do(t, r, "GET", "/fr/shop")

	if w.Code != http.StatusOK {
		t.Fatalf("状态 %d，body=%s —— 代理没打通，这条测试没测到东西", w.Code, w.Body.String())
	}
	if gotPath != "/fr/shop" {
		t.Errorf("上游收到 %q，want %q —— 语言前缀被网关吃掉了", gotPath, "/fr/shop")
	}
	if !strings.Contains(w.Body.String(), "upstream ok") {
		t.Errorf("响应体不是上游的: %s", w.Body.String())
	}
}

// 反过来:没有语言前缀的请求，转发路径必须一个字都不变。
func TestHttpReverseProxy_NoPrefixNoChange(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte("upstream ok"))
	}))
	defer upstream.Close()

	up, _ := url.Parse(upstream.URL)
	reg := memory.New()
	if err := reg.Register(&registry.Service{
		Name:  "gwsvc2",
		Nodes: []*registry.Node{{Id: "n1", Address: up.Host, Metadata: map[string]string{"protocol": "http"}}},
	}); err != nil {
		t.Fatal(err)
	}

	r := New(WithPathRewriter(langRewriter("fr")), WithRegistry(reg))
	defer r.Close()

	grp := NewGroup(WithAloneService(true, "gwsvc2"))
	grp.Url([]string{"GET"}, "/shop", HttpReverseProxy)
	r.RegisterGroup(grp)

	w := do(t, r, "GET", "/shop")

	if w.Code != http.StatusOK {
		t.Fatalf("状态 %d，body=%s", w.Code, w.Body.String())
	}
	if gotPath != "/shop" {
		t.Errorf("上游收到 %q，want %q", gotPath, "/shop")
	}
}
