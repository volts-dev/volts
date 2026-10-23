package router

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/volts-dev/volts/transport"
)

// bigXML 远超 net/http 的内部缓冲（约 4KB）：不带长度时一定以 chunked 发出。
func bigXML() string {
	return `<?xml version="1.0"?><urlset>` + strings.Repeat(`<url><loc>https://example.com/store/item</loc></url>`, 4000) + `</urlset>`
}

// TestRespond_SetsContentLength 钉的是 Respond 整块交来的正文带 Content-Length 出去。
//
// 不带的话它以 chunked 发出，而网关的 httputil.ReverseProxy 对没有长度的上游响应每读一块就
// Flush 一次 —— 网关上的压缩层遇 Flush 当场决定，第一块不够 minSize 就直通，于是同一个地址
// 有时压有时不压（见 TestCompress_ThroughProxyIsDeterministic）。
func TestRespond_SetsContentLength(t *testing.T) {
	payload := bigXML()
	do := liveRouter(t, nil, func(grp *TGroup) {
		grp.Url([]string{"GET"}, "/sitemap.xml", func(ctx *THttpContext) {
			ctx.Response().Header().Set("Content-Type", "application/xml")
			ctx.Respond([]byte(payload))
		})
		grp.Url([]string{"GET"}, "/declared", func(ctx *THttpContext) {
			// handler 自己声明的长度不动（哪怕它与正文不符也是 handler 的事，这里不替它猜）。
			ctx.Response().Header().Set("Content-Length", strconv.Itoa(len(payload)))
			ctx.Respond([]byte(payload))
		})
		grp.Url([]string{"GET"}, "/304", func(ctx *THttpContext) {
			ctx.WriteHeader(http.StatusNotModified)
		})
	})

	rsp := do("GET", "/sitemap.xml", nil)
	if rsp.ContentLength != int64(len(payload)) || len(rsp.TransferEncoding) != 0 {
		t.Fatalf("ContentLength=%d TransferEncoding=%v，要 %d 且不分块", rsp.ContentLength, rsp.TransferEncoding, len(payload))
	}
	if got := len(mustReadAll(t, rsp.Body)); got != len(payload) {
		t.Fatalf("正文 %d 字节，要 %d", got, len(payload))
	}
	if rsp := do("GET", "/declared", nil); rsp.ContentLength != int64(len(payload)) {
		t.Fatalf("handler 声明的长度：ContentLength=%d", rsp.ContentLength)
	}
	if rsp := do("GET", "/304", nil); rsp.StatusCode != http.StatusNotModified || rsp.Header.Get("Content-Length") != "" {
		t.Fatalf("304：status=%d Content-Length=%q", rsp.StatusCode, rsp.Header.Get("Content-Length"))
	}
}

// TestCompress_ThroughProxyIsDeterministic：上游不开压缩、网关开 WithCompress 并用 ReverseProxy 转发，
// 大正文每一次都要被压（真栈的网关 → websvc 就是这个形状）。
func TestCompress_ThroughProxyIsDeterministic(t *testing.T) {
	payload := bigXML()
	upstream := New()
	t.Cleanup(func() { close(upstream.exit) })
	ugrp := NewGroup()
	ugrp.Url([]string{"GET"}, "/sitemap.xml", func(ctx *THttpContext) {
		ctx.Response().Header().Set("Content-Type", "application/xml")
		ctx.Respond([]byte(payload))
	})
	upstream.RegisterGroup(ugrp)
	usrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstream.ServeHTTP(w, transport.NewHttpRequest(req))
	}))
	t.Cleanup(usrv.Close)
	target, _ := url.Parse(usrv.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)

	do := liveRouter(t, []Option{WithCompress()}, func(grp *TGroup) {
		grp.Url([]string{"GET"}, "/sitemap.xml", func(ctx *THttpContext) {
			proxy.ServeHTTP(ctx.Response(), ctx.Request().Request)
		})
	})
	for i := 0; i < 20; i++ {
		rsp := do("GET", "/sitemap.xml", map[string]string{"Accept-Encoding": "gzip"})
		if got := rsp.Header.Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("第 %d 次：Content-Encoding=%q，经网关的大正文要每次都压", i+1, got)
		}
	}
}
