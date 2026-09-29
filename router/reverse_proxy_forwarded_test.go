package router

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/registry/memory"
	"github.com/volts-dev/volts/transport"
)

/*
两跳反代之间 X-Forwarded-* 不许丢（见 internalHop 的注释）。

真栈上量到的形状（2026-09-29，站点应用挂载 P8）：网关 → websvc → kylin，kylin 渲出来的门户页里
og:image、hreflang 的主机全是 `http://127.0.0.1:36399`（websvc 的内网地址），同一页不经这一跳是
`http://localhost`。按域名分租户的部署上，属主进程拿这个地址去认租户会认错。
*/

type forwardedSeen struct{ host, proto, xff string }

var forwardedChainSeq int

func forwardedChain(t *testing.T) (*TRouter, *forwardedSeen) {
	t.Helper()
	seen := &forwardedSeen{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.host = r.Header.Get("X-Forwarded-Host")
		seen.proto = r.Header.Get("X-Forwarded-Proto")
		seen.xff = r.Header.Get("X-Forwarded-For")
		w.Write([]byte("ok"))
	}))
	t.Cleanup(upstream.Close)
	up, _ := url.Parse(upstream.URL)

	// 每条链一个服务名：选点 / 熔断的状态按服务名记在进程级，共用名字会选到上一条链已经
	// 关掉的上游（502）。
	forwardedChainSeq++
	svc := "fwdsvc" + strconv.Itoa(forwardedChainSeq)
	reg := memory.New()
	if err := reg.Register(&registry.Service{
		Name:  svc,
		Nodes: []*registry.Node{{Id: "n1", Address: up.Host, Metadata: map[string]string{"protocol": "http"}}},
	}); err != nil {
		t.Fatal(err)
	}
	r := New(WithRegistry(reg))
	t.Cleanup(r.Close)
	grp := NewGroup(WithAloneService(true, svc))
	grp.Url([]string{"GET"}, "/my/orders", HttpReverseProxy)
	r.RegisterGroup(grp)
	return r, seen
}

// hop 模拟"上一跳"送来的请求：Host 已被上一跳改写成本进程的地址。
func hop(t *testing.T, r *TRouter, remote string, depth int, hdr map[string]string) int {
	t.Helper()
	req := httptest.NewRequest("GET", "/my/orders", nil)
	req.Host = "127.0.0.1:36399"
	req.RemoteAddr = remote
	if depth > 0 {
		req.Header.Set(proxyDepthHeader, strconv.Itoa(depth))
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, transport.NewHttpRequest(req))
	return w.Code
}

var upstreamHeaders = map[string]string{
	"X-Forwarded-Host":  "shop.example",
	"X-Forwarded-Proto": "https",
	"X-Forwarded-For":   "203.0.113.9",
}

// 内部跳：上一跳写下的 Host / Proto 原样往下传，客户端 IP 链接着追加。
func TestReverseProxy_InternalHopKeepsForwardedHeaders(t *testing.T) {
	r, seen := forwardedChain(t)
	if code := hop(t, r, "127.0.0.1:50001", 1, upstreamHeaders); code != http.StatusOK {
		t.Fatalf("代理没打通（%d），这条测试没测到东西", code)
	}
	if seen.host != "shop.example" {
		t.Errorf("第二跳发出的 X-Forwarded-Host = %q，期望最初的 shop.example —— 属主进程会按内网地址认租户", seen.host)
	}
	if seen.proto != "https" {
		t.Errorf("X-Forwarded-Proto = %q，期望 https —— 页面里的绝对地址会降成 http", seen.proto)
	}
	if seen.xff != "203.0.113.9, 127.0.0.1" {
		t.Errorf("X-Forwarded-For = %q，期望客户端 IP 在前、这一跳追加在后", seen.xff)
	}
}

// 边缘跳：外部带来的 X-Forwarded-* 一律不认（与改之前逐字相同）。两种"不是内部跳"都要挡：
// 没有跳数头；有跳数头但对端是外网地址（跳数头客户端也能自己带）。
func TestReverseProxy_EdgeHopRewritesForwardedHeaders(t *testing.T) {
	for _, c := range []struct {
		name   string
		remote string
		depth  int
	}{
		{"没有跳数头", "127.0.0.1:50001", 0},
		{"外网对端自带跳数头", "192.0.2.7:50001", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, seen := forwardedChain(t)
			if code := hop(t, r, c.remote, c.depth, upstreamHeaders); code != http.StatusOK {
				t.Fatalf("代理没打通（%d）", code)
			}
			if seen.host != "127.0.0.1:36399" {
				t.Errorf("X-Forwarded-Host = %q，期望按这一跳的 Host 重写（外部带来的不认）", seen.host)
			}
			if seen.proto != "http" {
				t.Errorf("X-Forwarded-Proto = %q，期望 http", seen.proto)
			}
			host, _, _ := splitHostPortOrSelf(c.remote)
			if seen.xff != host {
				t.Errorf("X-Forwarded-For = %q，期望只有这一跳的对端 %q", seen.xff, host)
			}
		})
	}
}

func TestInternalHop(t *testing.T) {
	mk := func(remote string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		return r
	}
	for _, c := range []struct {
		remote string
		depth  int
		want   bool
	}{
		{"127.0.0.1:1", 1, true},
		{"[::1]:1", 2, true},
		{"10.1.2.3:1", 1, true},
		{"192.168.1.5:1", 1, true},
		{"127.0.0.1:1", 0, false},
		{"203.0.113.9:1", 1, false},
		{"pipe", 1, false},
	} {
		if got := internalHop(mk(c.remote), c.depth); got != c.want {
			t.Errorf("internalHop(%s, depth=%d) = %v，期望 %v", c.remote, c.depth, got, c.want)
		}
	}
}

func splitHostPortOrSelf(s string) (string, string, error) {
	u, err := url.Parse("http://" + s)
	if err != nil {
		return s, "", err
	}
	return u.Hostname(), u.Port(), nil
}
