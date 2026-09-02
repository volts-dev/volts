// Package ja3 为 volts 提供 TLS 指纹伪装的拨号实现。
//
// 它是**可选**子模块：主模块 volts 不依赖它，也就不依赖
// github.com/refraction-networking/utls —— 那是一个用途为模仿其他客户端 TLS
// 指纹的库，绝大多数写业务服务的人不需要它，却会被无条件编进二进制，
// 并在合规/安全扫描中被标记。
//
// 用法：空导入即自动注册
//
//	import _ "github.com/volts-dev/volts/core/ja3"
//
//	cli, _ := client.NewHttpClient(client.WithJa3("chrome_133", userAgent))
//
// 未导入本包却设置了指纹时，transport.Dial 会返回
// transport.ErrJa3NotRegistered —— 明确报错，而不是静默退回普通 TLS。
//
// # 指纹有两种写法，二选一
//
//	"chrome_133"                     预置名 —— 逐字节精确，**推荐**
//	"771,4865-4866-…,0-23-…,29-23,0" JA3 串 —— 近似重建，向后兼容
//
// 靠形状分辨：JA3 串必然是逗号分隔的五段。两者都不像就明确报错并列出可用预置名
// （写错一个下划线不该表现为一个看不懂的解析失败）。
//
// **能用预置名就别用 JA3 串。** 理由写在 profile.go 的包注释里，一句话是：JA3 不
// 记录扩展内容，而 GREASE、扩展乱序、后量子 key share、ALPS 全在内容里 —— 从 JA3
// 串重建出来的不是"像 Chrome"，是"一个谁都不是的客户端"。
package ja3

import (
	"crypto/tls"
	"fmt"
	"net"
	"strings"

	utls "github.com/refraction-networking/utls"
	"github.com/volts-dev/volts/transport"
)

func init() {
	transport.RegisterJa3Dialer(Dial)
}

// Dial 在已建立的 TCP 连接上按给定指纹完成 TLS 握手。
//
// spec 是预置名或 JA3 串，见包注释。失败时负责关闭 rawConn —— 调用方拿到 error
// 就不再持有该连接，不关就是每次握手失败泄漏一条。
func Dial(rawConn net.Conn, serverName string, cfg *tls.Config, spec string) (net.Conn, error) {
	spec = strings.TrimSpace(spec)

	if p, ok := LookupProfile(spec); ok {
		return dialProfile(rawConn, serverName, cfg, p)
	}
	if strings.Count(spec, ",") == 4 {
		return dialJA3(rawConn, serverName, cfg, spec)
	}

	rawConn.Close()
	return nil, unknownProfileError(spec)
}

// dialProfile 走 utls 的预置指纹：把 ClientHelloID 交给 utls，握手时它自己
// ApplyPreset。
//
// ★★ **这里不设 MinVersion/MaxVersion。** 预置 spec 自带 supported_versions
// 扩展（Chrome 133 会协商 TLS 1.3），在 config 上锁一个版本会与它打架 ——
// 而"指纹说我支持 1.3、config 说我最高 1.2"这种自相矛盾，正是伪装要避免的。
func dialProfile(rawConn net.Conn, serverName string, cfg *tls.Config, p *Profile) (net.Conn, error) {
	conn := utls.UClient(rawConn, utlsConfig(serverName, cfg), p.id)
	if err := conn.Handshake(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ja3: 用指纹 %s 握手失败: %w", p.Name, err)
	}
	return &uconn{UConn: conn}, nil
}

// dialJA3 走 JA3 串重建的那条路。
func dialJA3(rawConn net.Conn, serverName string, cfg *tls.Config, ja3 string) (net.Conn, error) {
	spec, err := parseJA3(ja3)
	if err != nil {
		rawConn.Close()
		return nil, err
	}

	// HelloCustom 是"spec 由调用方给全"的那个 id。
	//
	// ★ 这里曾经写的是 HelloChrome_Auto + ApplyPreset —— 能工作（ApplyPreset 会
	// 覆盖），但语义上是在说"用 Chrome 的预置，然后把它整个换掉"，而且
	// HelloChrome_Auto 随 utls 升级会指向不同版本，读代码的人无从判断到底用了谁。
	conn := utls.UClient(rawConn, utlsConfig(serverName, cfg), utls.HelloCustom)
	if err := conn.ApplyPreset(spec); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ja3: 应用 JA3 串失败: %w", err)
	}

	if err := conn.Handshake(); err != nil {
		conn.Close()
		if strings.Contains(err.Error(), "CurvePreferences includes unsupported curve") {
			return nil, fmt.Errorf("ja3: 握手失败（JA3 串里的曲线与 key_share 对不上，请改用预置名）: %w", err)
		}
		return nil, fmt.Errorf("ja3: 握手失败: %w", err)
	}
	return &uconn{UConn: conn}, nil
}

// utlsConfig 把调用方的 *tls.Config 转成 utls 的。
//
// **只取策略，不取版本**：ServerName 由参数给（拨号方知道自己在连谁），
// InsecureSkipVerify 与 RootCAs 由调用方决定，其余（版本、曲线偏好、密码套件）
// 一律交给指纹 —— 那些字段一旦设上就会改变 ClientHello，也就改变了指纹本身。
func utlsConfig(serverName string, cfg *tls.Config) *utls.Config {
	out := &utls.Config{ServerName: serverName}
	if cfg != nil {
		out.InsecureSkipVerify = cfg.InsecureSkipVerify
		out.RootCAs = cfg.RootCAs
	}
	return out
}

// uconn 让 utls 连接满足 transport.INegotiatedProtocol，
// 使上层无需断言 *utls.UConn 这个具体类型即可读取 ALPN 结果。
type uconn struct {
	*utls.UConn
}

func (c *uconn) NegotiatedProtocol() string {
	return c.ConnectionState().NegotiatedProtocol
}
