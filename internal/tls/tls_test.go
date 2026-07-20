package tls

import (
	"crypto/tls"
	"testing"
)

// Secure(nil) 必须给出「校验证书」的配置。
//
// 这条断言守的是一个真实退化过：broker/http、registry/consul、registry/etcd 三处
// 曾各自默认 &tls.Config{InsecureSkipVerify: true}，控制面因此可被中间人劫持 ——
// 篡改服务发现应答即可把集群 RPC 流量整体导向攻击者。
func TestSecureNilEnablesVerification(t *testing.T) {
	cfg := Secure(nil)

	if cfg == nil {
		t.Fatal("Secure(nil) 返回 nil，调用方会退回 Go 默认配置之外的未定义状态")
	}
	if cfg.InsecureSkipVerify {
		t.Error("Secure(nil) 不得跳过证书校验：跳过必须由调用方显式声明")
	}
	if cfg.MinVersion < tls.VersionTLS12 {
		t.Errorf("MinVersion = %#x, 要求 >= TLS1.2 (%#x)，RFC 8996 已弃用 1.0/1.1",
			cfg.MinVersion, tls.VersionTLS12)
	}
}

// 调用方显式要求跳过校验时必须被尊重 —— 本函数只提供安全默认，不做家长式覆盖。
func TestSecureRespectsExplicitInsecure(t *testing.T) {
	cfg := Secure(&tls.Config{InsecureSkipVerify: true})

	if !cfg.InsecureSkipVerify {
		t.Error("显式传入的 InsecureSkipVerify 被吞掉了")
	}
	if cfg.MinVersion < tls.VersionTLS12 {
		t.Error("即便跳过校验，MinVersion 仍应兜底到 TLS1.2")
	}
}

// MinVersion 只兜底，不下调调用方更高的要求。
func TestSecureDoesNotLowerMinVersion(t *testing.T) {
	cfg := Secure(&tls.Config{MinVersion: tls.VersionTLS13})

	if cfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %#x, 调用方设的 TLS1.3 被下调了", cfg.MinVersion)
	}
}

// 抬高 MinVersion 时不得就地改写调用方持有的对象 —— 同一份 *tls.Config 可能
// 被复用于多个连接，就地改写会造成难以追踪的串扰。
func TestSecureDoesNotMutateCaller(t *testing.T) {
	in := &tls.Config{MinVersion: tls.VersionTLS10}
	out := Secure(in)

	if in.MinVersion != tls.VersionTLS10 {
		t.Error("入参被就地改写")
	}
	if out.MinVersion != tls.VersionTLS12 {
		t.Errorf("返回值 MinVersion = %#x, 期望抬到 TLS1.2", out.MinVersion)
	}
}
