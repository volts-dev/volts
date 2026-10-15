package outbound

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"testing"
)

func TestIsPublicAddr(t *testing.T) {
	cases := map[string]bool{
		"8.8.8.8":          true,
		"2606:4700::1111":  true,
		"198.18.0.210":     true, // fake-IP 代理发的假地址，拦了开发机就一个网页都抓不了
		"2001:2::cd":       true,
		"127.0.0.1":        false,
		"::1":              false,
		"10.1.2.3":         false,
		"172.16.0.1":       false,
		"192.168.1.1":      false,
		"169.254.169.254":  false, // AWS / GCP 元数据
		"100.100.100.200":  false, // 阿里云元数据
		"0.0.0.0":          false,
		"0.1.2.3":          false,
		"255.255.255.255":  false,
		"fe80::1":          false,
		"fd00::1":          false,
		"::ffff:127.0.0.1": false, // IPv4 映射
		"64:ff9b::7f00:1":  false, // NAT64 里嵌着 127.0.0.1
		"64:ff9b::808:808": true,
		"224.0.0.1":        false,
	}
	for s, want := range cases {
		if got := isPublicAddr(netip.MustParseAddr(s)); got != want {
			t.Errorf("isPublicAddr(%s) = %v，想要 %v", s, got, want)
		}
	}
}

// TestPublicOnlyRefusesLoopback —— 标了 WithPublicOnly 的请求打不到本机；不标的照常。
//
// 先发不标的那个是有意的：它在连接池里留下一条通往 127.0.0.1 的空闲连接。第二个请求
// 复用这条连接就**不经过拨号**，拨号时的那道闸看不见它 —— 挡住它的只能是 RoundTrip
// 开头的检查。
func TestPublicOnlyRefusesLoopback(t *testing.T) {
	srv, seen := counting(t, func(int32) int { return http.StatusOK })
	cli := &http.Client{Transport: testRotatorN(1)}

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	rsp, err := cli.Do(req)
	if err != nil {
		t.Fatalf("不标 WithPublicOnly 的请求不该受影响: %v", err)
	}
	io.Copy(io.Discard, rsp.Body)
	rsp.Body.Close()

	req, _ = http.NewRequestWithContext(WithPublicOnly(context.Background()), http.MethodGet, srv.URL, nil)
	if rsp, err := cli.Do(req); err == nil {
		rsp.Body.Close()
		t.Fatal("WithPublicOnly 的请求连上了 127.0.0.1（复用了池里的空闲连接）")
	} else if !errors.Is(err, ErrNonPublicAddress) {
		t.Fatalf("错误不是 ErrNonPublicAddress: %v", err)
	}
	if seen.Load() != 1 {
		t.Fatalf("服务端收到 %d 个请求，想要 1（被拒的那个不该到达）", seen.Load())
	}
}

// TestPublicOnlyRefusesLocalhostName —— 主机名解析出来是环回的也拒（不只拦 IP 字面量）。
func TestPublicOnlyRefusesLocalhostName(t *testing.T) {
	err := checkPublicHost(context.Background(), "localhost")
	if !errors.Is(err, ErrNonPublicAddress) {
		t.Fatalf("localhost 没被拒: %v", err)
	}
}

// TestPublicOnlyDialControl —— 第二道闸单独成立：绕过 RoundTrip 的检查直接拨号，照样拒。
// 这一道防的是 DNS rebinding（检查时答公网、拨号时答环回），RoundTrip 那道看不见。
func TestPublicOnlyDialControl(t *testing.T) {
	srv, _ := counting(t, func(int32) int { return http.StatusOK })
	p := newPersona(nil, 0, nil, 1)
	addr := srv.Listener.Addr().String()

	if c, err := p.dialRaw(WithPublicOnly(context.Background()), "tcp", addr); err == nil {
		c.Close()
		t.Fatal("拨号时没拦住环回地址")
	} else if !errors.Is(err, ErrNonPublicAddress) {
		t.Fatalf("错误不是 ErrNonPublicAddress: %v", err)
	}
	c, err := p.dialRaw(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatalf("不标 WithPublicOnly 的拨号不该受影响: %v", err)
	}
	c.Close()
}
