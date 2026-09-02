package ja3

import (
	"crypto/tls"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// 对**真实公网站点**逐个指纹试握手 —— "这条指纹到底能不能用"的唯一凭据。
//
//	VOLTS_JA3_LIVE=1 go test ./ -run Live -v
//
// # 为什么本地那些绿灯不算数
//
// Go 的 TLS 服务端（httptest）比真实的宽容得多。2026-09-02 实测过一次代价：
// 一个违反 RFC 8446 §4.2.8 的 ClientHello（key_share 提了 supported_groups 里
// 没有的 group）在 httptest 上握手成功，在 www.ti.com（Akamai）上回
// `illegal parameter` —— 一个既不提 key_share 也不提 supported_groups 的错误。
// 在调用方那里，它与"网络不通"分不开。
//
// 所以这个文件是**边界探测器**：改动 parseJA3 或加一条预置指纹之后，手工跑一次。
// 它不进 CI（打别人家的站点，且会因为对方的网络状况红），但它是唯一能回答
// "我们发的 ClientHello 真实世界接不接受"的东西。
func TestFingerprintsAgainstLiveSites_Live(t *testing.T) {
	if os.Getenv("VOLTS_JA3_LIVE") == "" {
		t.Skip("设 VOLTS_JA3_LIVE=1 才会打真站点")
	}

	// 挑的是**三种不同的前端**：Akamai（TI，严格）、Cloudflare、Google。
	// 一条指纹在三家都能握手，才算真的可用。
	sites := []string{
		"www.ti.com:443", // Akamai —— 最严格的那个，见上面那段
		"www.cloudflare.com:443",
		"www.google.com:443",
	}

	for _, name := range Profiles() {
		for _, site := range sites {
			t.Run(name+"/"+strings.SplitN(site, ":", 2)[0], func(t *testing.T) {
				raw, err := net.DialTimeout("tcp", site, 10*time.Second)
				if err != nil {
					t.Skipf("拨不通（网络？）: %v", err)
				}
				defer raw.Close()
				raw.SetDeadline(time.Now().Add(15 * time.Second))

				host, _, _ := net.SplitHostPort(site)
				conn, err := Dial(raw, host, &tls.Config{}, name)
				if err != nil {
					t.Fatalf("✗ %v", err)
				}
				defer conn.Close()

				state := conn.(*uconn).ConnectionState()
				t.Logf("✓ TLS 0x%04x 套件 0x%04x ALPN %q",
					state.Version, state.CipherSuite, state.NegotiatedProtocol)

				// ALPN 协商不出 h2 是个信号：真实浏览器对这几家一定拿到 h2。
				// 拿不到通常意味着我们的 ALPN 扩展没发出去。
				if state.NegotiatedProtocol != "h2" {
					t.Errorf("ALPN 协商到 %q，想要 h2", state.NegotiatedProtocol)
				}
			})
		}
	}
}

// TestLegacyJA3StringsAgainstLiveSites_Live —— JA3 串那条路的同款探测。
//
// 留着是因为 parseJA3 的改动最容易在这里炸：它重建的 ClientHello 是**拼出来的**，
// 每一个"看起来合理"的补充（补一个 GREASE、补一条 key_share）都可能造出一个
// 真实服务器拒收的组合。
func TestLegacyJA3StringsAgainstLiveSites_Live(t *testing.T) {
	if os.Getenv("VOLTS_JA3_LIVE") == "" {
		t.Skip("设 VOLTS_JA3_LIVE=1 才会打真站点")
	}

	for i, ja3 := range []string{
		// 曲线段没有 GREASE —— 就是当初在 ti.com 上炸掉的那条。
		"771,4866-4867-4865-49196-49200-159-52393-52392-52394-49195-49199-158-49188-49192-107-49187-49191-103-49162-49172-57-49161-49171-51-157-156-61-60-53-47-255,0-11-10-35-16-22-23-13-43-45-51-21,29-23-1035-25-24,0-1-2",
		"771,4865-4866-4867-49195-49199-49196-49200-52393-52392-49171-49172-156-157-47-53,0-23-65281-10-11-35-16-5-13-18-51-45-43-27-21,29-23-24,0",
	} {
		raw, err := net.DialTimeout("tcp", "www.ti.com:443", 10*time.Second)
		if err != nil {
			t.Skipf("拨不通: %v", err)
		}
		raw.SetDeadline(time.Now().Add(15 * time.Second))

		conn, err := Dial(raw, "www.ti.com", &tls.Config{}, ja3)
		if err != nil {
			raw.Close()
			t.Errorf("第 %d 条 ✗ %v", i+1, err)
			continue
		}
		t.Logf("第 %d 条 ✓ TLS 0x%04x ALPN %q", i+1,
			conn.(*uconn).ConnectionState().Version, conn.(*uconn).ConnectionState().NegotiatedProtocol)
		conn.Close()
	}
}
