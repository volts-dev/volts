package client

import (
	"context"

	"github.com/volts-dev/volts/errors"
)

// note that returning either false or a non-nil error will result in the call not being retried
type RetryFunc func(ctx context.Context, req IRequest, retryCount int, err error) (bool, error)

// RetryNever 从不重试。这是本框架的默认策略。
//
// **重试一个非幂等调用等于让它执行两次。** 本仓的调用方里有下单、扣款、记账，
// "宁可失败一次，也不能悄悄执行两次" —— 所以框架不替调用方做这个决定，要重试
// 必须由知道自己幂等性的那一方明说（`client.WithRetries(n)` + 选一个策略）。
func RetryNever(ctx context.Context, req IRequest, retryCount int, err error) (bool, error) {
	return false, nil
}

// RetryAlways always retry on error
func RetryAlways(ctx context.Context, req IRequest, retryCount int, err error) (bool, error) {
	return true, nil
}

// RetryOnConnectError 只在**连接根本没建立起来**时重试。
//
// 这是唯一可以证明安全的一档：连接都没建成，请求的一个字节都没有发出去，服务端
// 不可能执行过它 —— 重试只是换一个节点再试（selector 的 Next 每次给出下一个节点），
// 不存在重复执行。
//
// 明确**不**包含的两种，即便它们看起来也像"没成功"：
//
//   - 发送成功、读响应时断开（裸 EOF）：请求已经写出去了，无法区分"服务端没收到"
//     与"服务端处理完才断开"。这与反向代理那边不用 Idempotency-Key 一把梭是同一条
//     理由。
//   - 服务端回了 5xx：那是服务端**执行过**并失败了，重试就是再执行一次。
//
// RPC 客户端在服务端答复了非 OK 状态位时会直接返回、根本不走重试，所以这一档在
// RPC 侧覆盖的正是全部可重试情形。HTTP 客户端不标记这个哨兵（net/http 自己已经对
// 可重放的请求做了连接级重试），因此本策略在 HTTP 侧等价于不重试。
func RetryOnConnectError(ctx context.Context, req IRequest, retryCount int, err error) (bool, error) {
	if err == nil {
		return false, nil
	}
	return errors.Is(err, ErrConnectFailed), nil
}

// RetryOnError 在超时或 500 时重试。
//
// ⚠️ 只适合**幂等**调用：500 意味着服务端已经执行过，重试是再执行一次；408 则连
// "执行过没有"都不知道。写路径请改用 RetryOnConnectError。
//
// 此前这个函数是死的：它拿 `errors.Parse(err.Error())` 取码，而 `Error()` 只返回
// Detail —— Parse 对一句普通错误文案做 JSON 解码必然失败，于是 Code 恒为 0，
// switch 一档都不匹配，**永远返回 false**。谁把 Retries 调大都不会有第二次尝试，
// 而且没有任何提示。现在按错误链取码（errors.Code），包装过的也认。
func RetryOnError(ctx context.Context, req IRequest, retryCount int, err error) (bool, error) {
	if err == nil {
		return false, nil
	}

	switch errors.Code(err) {
	// retry on timeout or internal server error
	case 408, 500:
		return true, nil
	default:
		return false, nil
	}
}
