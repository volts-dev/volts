package outbound

import (
	"context"
	"net/http"
	"testing"

	"golang.org/x/net/proxy"
)

// TestExitRecordsTheIdentityThatAnswered —— 被挡换身份之后，Exit 里留下的必须是
// **给出最终答复的那一个**，不是第一个。
//
// 调用方（connect_components 的元件监测）拿它去落"这次是从哪个 IP 问的"。记成第一个
// 的话，那一行写的恰恰是被封掉的那个出口 —— 人会去换一个其实好好的节点。
func TestExitRecordsTheIdentityThatAnswered(t *testing.T) {
	srv, _ := counting(t, func(n int32) int {
		if n == 1 {
			return http.StatusForbidden
		}
		return http.StatusOK
	})

	r := testRotatorN(2)
	// 两个身份都"经代理"出门；拨号器其实是直连，这里只关心记下来的是哪个 URL。
	r.personas[0].out = &proxyEndpoint{url: "socks5://first:1080", dialer: proxy.Direct}
	r.personas[1].out = &proxyEndpoint{url: "socks5://second:1080", dialer: proxy.Direct}

	ctx, exit := WithExit(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	rsp, err := (&http.Client{Transport: r}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	rsp.Body.Close()

	got, direct, ok := exit.Get()
	if !ok || direct || got != "socks5://second:1080" {
		t.Fatalf("Exit = (%q, direct=%v, ok=%v)，想要第二个身份的代理 URL", got, direct, ok)
	}
	if exit.Identity() != "B" {
		t.Errorf("Identity = %q，想要 B", exit.Identity())
	}
}

// TestExitReportsDirect —— 直连的身份报 direct=true、URL 为空；没挂 Exit 的 ctx 照常发。
func TestExitReportsDirect(t *testing.T) {
	srv, _ := counting(t, func(int32) int { return http.StatusOK })
	cli := &http.Client{Transport: testRotatorN(1)}

	ctx, exit := WithExit(context.Background())
	if _, _, ok := exit.Get(); ok {
		t.Fatal("还没发请求，Exit 就报 ok")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	rsp, err := cli.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	rsp.Body.Close()

	if got, direct, ok := exit.Get(); !ok || !direct || got != "" {
		t.Fatalf("Exit = (%q, direct=%v, ok=%v)，想要直连", got, direct, ok)
	}

	// 不挂 Exit 的请求不受影响。
	rsp, err = cli.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	rsp.Body.Close()

	var nilExit *Exit
	if _, _, ok := nilExit.Get(); ok {
		t.Error("nil Exit 报 ok")
	}
}
