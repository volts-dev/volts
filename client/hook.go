package client

import (
	"context"
	"sync"
)

// RequestHook 在每次出站请求发出前调用一次。
//
// **调用时机在选节点与重试之前**，所以每个请求只跑一次，不是每次重试跑一次 ——
// 这与被删掉的那个 `CallWrappers` 半成品不同：后者想包住 HttpClient/RpcClient 各自的
// 低层 `call`，而两者返回的是不同的具体类型（*httpResponse / *rpcResponse），
// 用一个类型套不住，且身份这类横切信息在重试循环内部重复施加也没有意义。
// 钩子改挂在两条链路**共用**的 IRequest 上，问题自然消失。
//
// 存在的理由是「横切信息只能有一份实现」：调用方身份、追踪 id、租户上下文这类东西
// 一旦散落到每个调用点各写一遍，就一定会有漏的地方，而漏掉的表现往往是 401 或者
// 更糟的「悄悄用了错的身份」。
//
// 两条注入路径都可用：
//   - `req.Header().Set(k, v)` —— 直接盖在出站请求头上；HTTP 与 RPC 都生效
//     （RpcClient 会把 req.Header() 灌进 wire 帧头，见 rpc_client.go 的 init header）。
//   - 返回一个带 metadata 的新 ctx —— RpcClient 会把 `metadata.FromContext(ctx)`
//     的每一项也灌进帧头。
//
// 返回的 ctx 替换后续调用使用的 ctx；返回 error 直接中止本次请求（**不重试**）——
// 身份盖不上就不该把请求发出去。
type RequestHook func(ctx context.Context, req IRequest) (context.Context, error)

var (
	globalHooksMu sync.RWMutex
	globalHooks   []RequestHook
)

// RegisterGlobalRequestHook 注册一个进程级钩子，对**本进程创建的所有客户端**生效。
//
// 服务身份属于这一类：一个进程只有一个身份，不应该要求每处 New 客户端时都记得传。
// 进程级钩子先于该客户端自己的 Config.RequestHooks 执行。
//
// 只在启动阶段调用（与 codec.RegisterCodec 等同款约定）；运行期注册虽然加了锁不会
// 数据竞争，但会让「同一时刻不同请求带不同身份」这种难查的现象成为可能。
func RegisterGlobalRequestHook(hooks ...RequestHook) {
	globalHooksMu.Lock()
	defer globalHooksMu.Unlock()
	globalHooks = append(globalHooks, hooks...)
}

// ResetGlobalRequestHooks 清空进程级钩子。给测试用 —— 全局状态若不能复位，
// 同一个包里的用例就会互相污染。
func ResetGlobalRequestHooks() {
	globalHooksMu.Lock()
	defer globalHooksMu.Unlock()
	globalHooks = nil
}

// applyRequestHooks 依次执行进程级钩子与客户端级钩子，返回可能被替换过的 ctx。
// 任一钩子报错即整体失败。
func applyRequestHooks(ctx context.Context, req IRequest, own []RequestHook) (context.Context, error) {
	globalHooksMu.RLock()
	global := globalHooks
	globalHooksMu.RUnlock()

	if len(global) == 0 && len(own) == 0 {
		return ctx, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	for _, h := range global {
		if h == nil {
			continue
		}
		newCtx, err := h(ctx, req)
		if err != nil {
			return ctx, err
		}
		if newCtx != nil {
			ctx = newCtx
		}
	}
	for _, h := range own {
		if h == nil {
			continue
		}
		newCtx, err := h(ctx, req)
		if err != nil {
			return ctx, err
		}
		if newCtx != nil {
			ctx = newCtx
		}
	}
	return ctx, nil
}
