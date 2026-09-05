package client

import (
	stderrors "errors"
	"net"

	"github.com/volts-dev/volts/errors"
)

// ErrConnectFailed 表示**连接根本没有建立起来**（拨号失败 / 连接池取不到可用连接）。
//
// 它的全部意义在于把"请求一个字节都没发出去"与"发出去了但没等到回应"分开：前者
// 重试是安全的（服务端不可能执行过），后者不是。判定用 errors.Is：
//
//	if errors.Is(err, client.ErrConnectFailed) { /* 换个节点重试 */ }
//
// 参见 RetryOnConnectError。
var ErrConnectFailed = errors.New("volts.client", 503, "connection could not be established")

// ErrNoAvailableNode 表示选不出可用节点（服务未注册、全部节点被熔断摘除、
// 或过滤条件把候选集清空了）。同样属于"请求没发出去"。
var ErrNoAvailableNode = errors.New("volts.client", 503, "no available node for service")

// isConnectFailure 判断一个出站错误是否属于「连接根本没有建立起来」。
//
// 判据只认**拨号阶段**：`net.OpError.Op == "dial"` 是标准库给拨号失败打的标记
// （连接被拒、主机不可达、DNS 解析不出来都在里面），而读写阶段的失败拿到的是
// `Op == "read"` / `"write"`。`http.Client.Do` 把底层错误包在 `*url.Error` 里，
// errors.As 会穿过去。
//
// **只认可以证明的那一档，认不出就当作不可重试。** 判错的两个方向代价完全不对等：
// 漏判只是少一次重试，误判则是把一个可能已经执行过的请求再发一遍 —— 本仓有下单和
// 扣款。所以这里刻意不去匹配裸 EOF、`http: server closed idle connection` 之类的
// 文本：那些要么语义本身就含糊（请求已写出，服务端可能处理完才断开），要么依赖
// 标准库未导出的措辞。
//
// 自定义拨号器（本仓的 SOCKS5 / ss / ssr）返回的错误未必包成 net.OpError，于是
// 认不出来 —— 那正好落在安全的一侧。
func isConnectFailure(err error) bool {
	if err == nil {
		return false
	}
	var opErr *net.OpError
	if stderrors.As(err, &opErr) && opErr.Op == "dial" {
		return true
	}
	var dnsErr *net.DNSError
	return stderrors.As(err, &dnsErr)
}
