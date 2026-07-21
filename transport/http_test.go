package transport

import (
	"crypto/tls"
	"testing"
)

// h2c（明文连接上的 HTTP/2）默认必须关闭。
//
// 此前非 TLS 监听会无条件包上 h2c handler。h2c 的明文升级
// （Upgrade: h2c / PRI * HTTP/2.0）在有前置代理的部署里是请求走私的经典载体：
// 前端代理按 HTTP/1.1 理解报文、后端按 HTTP/2 帧理解，两者对"一个请求到哪里
// 结束"产生分歧就能跨请求注入。这类能力必须是显式选择，不能白送。
func TestH2CDisabledByDefault(t *testing.T) {
	cfg := newConfig()

	if cfg.EnableH2C {
		t.Fatal("EnableH2C 默认必须为 false —— h2c 是请求走私的常见载体，不能默认开启")
	}
}

func TestEnableH2COption(t *testing.T) {
	cfg := newConfig(EnableH2C(true))

	if !cfg.EnableH2C {
		t.Fatal("EnableH2C(true) 未生效")
	}
}

// TLS 相关默认值：不跳过校验、由 listen 时按安全默认构造。
// 与 internal/tls.Secure 的策略一致，防止后人改回不安全的默认。
func TestTransportTLSDefaults(t *testing.T) {
	cfg := newConfig()

	if cfg.Secure {
		t.Error("Secure 默认应为 false（由调用方显式开启）")
	}
	if cfg.TlsConfig != nil {
		t.Error("TlsConfig 默认应为 nil，由 listen 时按安全默认构造")
	}
}

// 显式传入的 TLS 配置必须被保留，不能被框架覆写。
func TestTLSConfigOptionRespected(t *testing.T) {
	in := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}
	cfg := newConfig(TLSConfig(in))

	if cfg.TlsConfig != in {
		t.Fatal("显式传入的 TlsConfig 被替换了")
	}
	if !cfg.TlsConfig.InsecureSkipVerify {
		t.Error("调用方显式声明的 InsecureSkipVerify 被吞掉")
	}
	if cfg.TlsConfig.MinVersion != tls.VersionTLS13 {
		t.Error("调用方设定的 MinVersion 被改动")
	}
}
