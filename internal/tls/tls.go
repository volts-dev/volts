package tls

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"time"
)

func Certificate(host ...string) (tls.Certificate, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}

	notBefore := time.Now()
	notAfter := notBefore.Add(time.Hour * 24 * 365)

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return tls.Certificate{}, err
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Acme Co"},
		},
		NotBefore: notBefore,
		NotAfter:  notAfter,

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	for _, h := range host {
		if ip := net.ParseIP(h); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, h)
		}
	}

	template.IsCA = true
	template.KeyUsage |= x509.KeyUsageCertSign

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, err
	}

	// create public key
	certOut := bytes.NewBuffer(nil)
	pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes})

	// create private key
	keyOut := bytes.NewBuffer(nil)
	b, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: b})

	return tls.X509KeyPair(certOut.Bytes(), keyOut.Bytes())
}

// Secure 返回一份可安全用于出站连接的 TLS 配置。
//
// 存在的理由：broker/http、registry/consul、registry/etcd 三处此前在调用方未提供
// TLS 配置时，各自默认 &tls.Config{InsecureSkipVerify: true}。控制面被中间人劫持
// 的后果比业务连接严重得多 —— 篡改服务发现应答即可把整个集群的 RPC 流量导向攻击者
// 节点。现统一走本函数：
//
//   - cfg 为 nil        → 开启证书校验，MinVersion = TLS 1.2
//   - cfg 已给出        → 原样保留（含调用方显式声明的 InsecureSkipVerify），
//     仅在 MinVersion 未设置或低于 1.2 时兜底抬高（RFC 8996 已弃用 TLS 1.0/1.1）
//
// 需要跳过校验的用户必须显式传入 &tls.Config{InsecureSkipVerify: true}，
// 由其自行承担风险。返回值与入参不共享底层对象（必要时 Clone），
// 避免就地改写调用方持有的配置。
func Secure(cfg *tls.Config) *tls.Config {
	if cfg == nil {
		return &tls.Config{MinVersion: tls.VersionTLS12}
	}
	if cfg.MinVersion < tls.VersionTLS12 {
		cfg = cfg.Clone()
		cfg.MinVersion = tls.VersionTLS12
	}
	return cfg
}
