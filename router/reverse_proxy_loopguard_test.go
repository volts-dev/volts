package router

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/volts-dev/volts/internal/addr"
	"github.com/volts-dev/volts/transport"
)

// isLoopbackHost 必须认全 loopback 的各种写法，且不误判外网/局域网地址。
func TestIsLoopbackHost(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.0.0.53", true}, // 整个 127.0.0.0/8 都是 loopback
		{"::1", true},
		{"localhost", true},
		{"0.0.0.0", false}, // 通配符不是 loopback
		{"192.168.1.5", false},
		{"10.0.0.1", false},
		{"example.com", false}, // 非 IP、非 localhost
		{"", false},
	}
	for _, c := range cases {
		if got := isLoopbackHost(c.host); got != c.want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

// isSelfTarget 的判据是“端口等于本机监听端口 + 主机指向本机（loopback 或本机网卡 IP）”。
func TestIsSelfTarget(t *testing.T) {
	listen := &net.TCPAddr{IP: net.IPv6zero, Port: 35999} // 监听 [::]:35999

	cases := []struct {
		name   string
		target string
		listen net.Addr
		want   bool
	}{
		{"loopback 同端口即自身", "127.0.0.1:35999", listen, true},
		{"::1 同端口即自身", "[::1]:35999", listen, true},
		{"localhost 同端口即自身", "localhost:35999", listen, true},
		{"loopback 但端口不同", "127.0.0.1:45999", listen, false},
		{"非本机地址同端口不算自环", "192.0.2.1:35999", listen, false}, // RFC 5737 文档地址，不会是本机
		{"拿不到监听地址时保守放行", "127.0.0.1:35999", nil, false},
		{"目标地址非法", "not-an-addr", listen, false},
	}
	for _, c := range cases {
		if got := isSelfTarget(c.target, c.listen); got != c.want {
			t.Errorf("%s: isSelfTarget(%q,%v) = %v, want %v", c.name, c.target, c.listen, got, c.want)
		}
	}
}

// 护盾必须认出「本机网卡 IP + 同端口」这种自环 —— 注册中心里存的正是网卡地址
// （192.168.x.x:35999），而不是 loopback。此前只认 loopback，一旦上游
// addr.LocalFormat 的折叠失效（IP 缓存 TTL 内网络刚切换、或注册的是僵尸节点的旧
// 地址），目标就以网卡 IP 原样进来，护盾被整个绕过，请求真的拨回自己。
func TestIsSelfTarget_LocalNICAddressIsSelf(t *testing.T) {
	var lan net.IP
	for _, ip := range addr.IPs() {
		if ip.To4() != nil && !ip.IsLoopback() {
			lan = ip
			break
		}
	}
	if lan == nil {
		t.Skip("本机没有非 loopback 的 IPv4 网卡地址，跳过")
	}

	listen := &net.TCPAddr{IP: net.IPv6zero, Port: 35999}

	if !isSelfTarget(net.JoinHostPort(lan.String(), "35999"), listen) {
		t.Fatalf("本机网卡地址 %s:35999 与监听端口相同，必须判为自环", lan)
	}
	// 端口不同则不是自身：同一台机器上别的进程完全可以在另一个端口提供服务。
	if isSelfTarget(net.JoinHostPort(lan.String(), "45999"), listen) {
		t.Fatalf("本机网卡地址 %s:45999 端口不同，不该判为自环", lan)
	}
}

// proxyDepthOf 对缺失 / 非法 / 负数一律按 0，正常值原样返回。
func TestProxyDepthOf(t *testing.T) {
	mk := func(v string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		if v != "" {
			r.Header.Set(proxyDepthHeader, v)
		}
		return r
	}
	cases := []struct {
		val  string
		want int
	}{
		{"", 0},         // 头缺失
		{"0", 0},        //
		{"3", 3},        //
		{"-1", 0},       // 负数按 0
		{"abc", 0},      // 非法按 0
		{"2147483648x", 0},
	}
	for _, c := range cases {
		if got := proxyDepthOf(mk(c.val)); got != c.want {
			t.Errorf("proxyDepthOf(%q) = %d, want %d", c.val, got, c.want)
		}
	}
	if got := proxyDepthOf(nil); got != 0 {
		t.Errorf("proxyDepthOf(nil) = %d, want 0", got)
	}
}

// localListenAddr 从 stdlib 注入的 http.LocalAddrContextKey 取出监听地址；缺失返回 nil。
func TestLocalListenAddr(t *testing.T) {
	want := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 35999}

	// 模拟 stdlib http.Server 在每条连接上注入的 LocalAddrContextKey。
	r := httptest.NewRequest("GET", "/", nil)
	r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, want))
	if got := localListenAddr(r); got == nil || got.String() != want.String() {
		t.Fatalf("localListenAddr = %v, want %v", got, want)
	}

	// httptest.NewRequest 默认不带该 key —— 取不到时必须是 nil，而非 panic。
	if got := localListenAddr(httptest.NewRequest("GET", "/", nil)); got != nil {
		t.Fatalf("localListenAddr(无 key) = %v, want nil", got)
	}
	if got := localListenAddr(nil); got != nil {
		t.Fatalf("localListenAddr(nil) = %v, want nil", got)
	}
}

// 端到端：跳数触顶时，HttpReverseProxy 必须直接 508 并短路 —— 连节点都不选、不拨号。
// 这是任何转发环（自环或跨节点）耗尽临时端口前的最后封顶。
func TestHttpReverseProxy_DepthGuardReturns508(t *testing.T) {
	r := New()
	defer r.Close()

	grp := NewGroup()
	grp.Url([]string{"POST"}, "/proxy-loop", HttpReverseProxy)
	r.RegisterGroup(grp)

	req := httptest.NewRequest("POST", "/proxy-loop", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(proxyDepthHeader, strconv.Itoa(maxProxyDepth)) // 已触顶
	w := httptest.NewRecorder()

	r.ServeHTTP(w, transport.NewHttpRequest(req))

	if w.Code != http.StatusLoopDetected {
		t.Fatalf("触顶请求应返回 508 Loop Detected，实际 %d", w.Code)
	}
}

// 真实 http.Server 端到端：验证 stdlib 注入的 http.LocalAddrContextKey 能一路
// 保留到 volts 的 handler 里 —— 自身地址短路护盾正是靠它拿到本进程监听地址。
// 这里复刻生产里 transport 的 customxx 适配（NewHttpRequest 包一层交给 router）。
func TestLocalListenAddr_SurvivesRealServingPath(t *testing.T) {
	r := New()
	defer r.Close()

	var gotPort string
	grp := NewGroup()
	grp.Url([]string{"GET"}, "/probe", func(c *THttpContext) {
		if la := localListenAddr(c.Request().Request); la != nil {
			if _, p, err := net.SplitHostPort(la.String()); err == nil {
				gotPort = p
			}
		}
		c.WriteHeader(http.StatusOK)
	})
	r.RegisterGroup(grp)

	// stdlib http.Server 会在每条连接的请求 context 上注入 LocalAddrContextKey。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.ServeHTTP(w, transport.NewHttpRequest(req))
	}))
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/probe")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	_, wantPort, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if gotPort == "" {
		t.Fatal("handler 里 localListenAddr 取不到监听地址 —— 自身地址护盾在真实路径上会失效")
	}
	if gotPort != wantPort {
		t.Fatalf("localListenAddr 端口 = %s, 期望 = %s（server 实际监听端口）", gotPort, wantPort)
	}
}

// 未触顶时不应被跳数护盾拦截（应继续走后续逻辑，而非 508）。
func TestHttpReverseProxy_DepthUnderLimitNotBlocked(t *testing.T) {
	r := New()
	defer r.Close()

	grp := NewGroup()
	grp.Url([]string{"POST"}, "/proxy-ok", HttpReverseProxy)
	r.RegisterGroup(grp)

	req := httptest.NewRequest("POST", "/proxy-ok", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(proxyDepthHeader, strconv.Itoa(maxProxyDepth-1)) // 差一跳触顶
	w := httptest.NewRecorder()

	r.ServeHTTP(w, transport.NewHttpRequest(req))

	// 没有可用节点会走 404/500 等其它分支，但绝不该是跳数护盾的 508。
	if w.Code == http.StatusLoopDetected {
		t.Fatalf("未触顶请求被跳数护盾误拦为 508")
	}
}
