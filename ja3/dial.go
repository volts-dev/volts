// Package ja3 为 volts 提供 JA3（TLS 指纹伪装）拨号实现。
//
// 它是**可选**子模块：主模块 volts 不依赖它，也就不依赖
// github.com/refraction-networking/utls —— 那是一个用途为模仿其他客户端 TLS
// 指纹的库，绝大多数写业务服务的人不需要它，却会被无条件编进二进制，
// 并在合规/安全扫描中被标记。
//
// 用法：空导入即自动注册
//
//	import _ "github.com/volts-dev/volts/ja3"
//
//	cli, _ := client.NewHttpClient(client.WithJa3(spec, userAgent))
//
// 未导入本包却设置了 JA3 指纹时，transport.Dial 会返回
// transport.ErrJa3NotRegistered —— 明确报错，而不是静默退回普通 TLS。
package ja3

import (
	"crypto/tls"
	"fmt"
	"net"

	utls "github.com/refraction-networking/utls"
	"github.com/volts-dev/volts/transport"
)

func init() {
	transport.RegisterJa3Dialer(Dial)
}

// Dial 在已建立的 TCP 连接上按给定 JA3 指纹完成 TLS 握手。
//
// 失败时负责关闭 rawConn —— 调用方拿到 error 就不再持有该连接，
// 不关就是每次握手失败泄漏一条。
func Dial(rawConn net.Conn, serverName string, cfg *tls.Config, ja3 string) (net.Conn, error) {
	spec, err := parseJA3(ja3)
	if err != nil {
		rawConn.Close()
		return nil, err
	}

	insecure := false
	if cfg != nil {
		insecure = cfg.InsecureSkipVerify
	}

	conn := utls.UClient(rawConn, &utls.Config{
		ServerName: serverName,
		MinVersion: tls.VersionTLS12,
		// 指纹伪装要求 ClientHello 与被模仿的客户端一致，这里锁在 1.2：
		// 放开到 1.3 会让 utls 追加 1.3 专属扩展，指纹随之改变。
		MaxVersion:         tls.VersionTLS12,
		InsecureSkipVerify: insecure,
	}, utls.HelloChrome_Auto)

	if err := conn.ApplyPreset(spec); err != nil {
		conn.Close()
		return nil, err
	}

	if err := conn.Handshake(); err != nil {
		conn.Close()
		if err.Error() == "tls: CurvePreferences includes unsupported curve" {
			return nil, fmt.Errorf("ja3: TLS 1.3 握手失败（请重试）: %w", err)
		}
		return nil, fmt.Errorf("ja3: 握手失败: %w", err)
	}

	return &uconn{UConn: conn}, nil
}

// uconn 让 utls 连接满足 transport.INegotiatedProtocol，
// 使上层无需断言 *utls.UConn 这个具体类型即可读取 ALPN 结果。
type uconn struct {
	*utls.UConn
}

func (c *uconn) NegotiatedProtocol() string {
	return c.ConnectionState().NegotiatedProtocol
}
