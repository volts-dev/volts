package client

import "github.com/volts-dev/volts/errors"

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
