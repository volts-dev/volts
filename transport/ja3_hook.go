package transport

import (
	"crypto/tls"
	"errors"
	"net"
	"sync"
)

// JA3（TLS 指纹伪装）的挂载点。
//
// 实现放在独立子模块 github.com/volts-dev/volts/ja3 里，本包只保留注册表。
// 这么切的理由：
//   - JA3 依赖 github.com/refraction-networking/utls —— 一个用途是**模仿其他
//     客户端的 TLS 指纹**的库。绝大多数用 volts 写业务服务的人不需要它，却会
//     无条件把它编进二进制，并在合规/安全扫描里被标记
//   - 它是可选能力，不该成为框架核心的强依赖
//
// 用法：需要 JA3 的程序空导入子模块即可自动注册
//
//	import _ "github.com/volts-dev/volts/ja3"
//
// 未导入却设置了 JA3 指纹时，Dial 会返回 ErrJa3NotRegistered —— 明确报错，
// 而不是**静默退回普通 TLS**：后者会让指纹伪装悄悄失效，调用方以为自己伪装了。

// Ja3 描述一次 JA3 指纹伪装的参数。
//
// 只是几个字符串，不牵涉 utls，因此留在本包：DialConfig 与 client.Config 都要
// 引用它，把它挪进子模块会让主模块反过来依赖可选模块。
type Ja3 struct {
	Ja3       string
	UserAgent string
	Hash      string
}

// Ja3DialFunc 用给定的 JA3 指纹在已建立的 TCP 连接上完成 TLS 握手。
//
// rawConn 是底层 TCP 连接（实现方负责在失败时关闭它）；serverName 用于 SNI；
// cfg 提供 InsecureSkipVerify 等策略；ja3 是指纹串。
type Ja3DialFunc func(rawConn net.Conn, serverName string, cfg *tls.Config, ja3 string) (net.Conn, error)

// ErrJa3NotRegistered 表示设置了 JA3 指纹但没有导入实现子模块。
var ErrJa3NotRegistered = errors.New(
	"transport: 设置了 JA3 指纹但未注册实现 —— 请导入 _ \"github.com/volts-dev/volts/ja3\"")

var (
	ja3Mu   sync.RWMutex
	ja3Dial Ja3DialFunc
)

// RegisterJa3Dialer 注册 JA3 握手实现。由 ja3 子模块在 init 中调用。
func RegisterJa3Dialer(fn Ja3DialFunc) {
	ja3Mu.Lock()
	ja3Dial = fn
	ja3Mu.Unlock()
}

// Ja3Registered 报告是否已注册 JA3 实现。
func Ja3Registered() bool {
	ja3Mu.RLock()
	defer ja3Mu.RUnlock()
	return ja3Dial != nil
}

func ja3Dialer() Ja3DialFunc {
	ja3Mu.RLock()
	defer ja3Mu.RUnlock()
	return ja3Dial
}

// INegotiatedProtocol 由完成了 ALPN 协商的 TLS 连接实现。
//
// 用于替代对具体 TLS 实现类型的断言：调用方只关心"协商出了什么协议"，
// 不该被绑死在 crypto/tls 或 utls 的某个具体类型上。
type INegotiatedProtocol interface {
	NegotiatedProtocol() string
}
