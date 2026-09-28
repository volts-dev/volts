package outbound

import (
	"context"
	"sync"
)

// Exit 记下一次出站请求**实际**从哪个出口出去的。
//
// 存在的理由：轮换是本包内部的事，调用方拿到的只是一个 *http.Response —— 它不知道
// 这一次落在了哪个代理上。可"被挡了"这件事恰恰要按出口去读：对方按 IP 封，那么
// "哪一次、从哪个 IP、被回了 403" 是唯一能指着说"换掉这个节点"的凭据。
//
//	ctx, exit := outbound.WithExit(ctx)
//	rsp, err := outbound.Client().Do(req.WithContext(ctx))
//	proxyURL, direct, ok := exit.Get()
//
// ★ 记的是**最后一次尝试**用的身份：被挡换身份重试、跟重定向，都会覆盖前一次。
// 对成功的请求与"全部被挡、交还最后那个 403"的请求，这正是给出那个响应的出口；
// 对"全部连不上"返回的错误，它是最后试的那个，不一定是错误信息里说的那个。
//
// ★ 本包**不知道出口 IP**：代理的出口地址只有经它问一次回显服务才量得出来，每个
// 请求都去量一次代价不值。这里给的是**代理 URL**（与传给 SetProxies 的那份逐字相同，
// **带口令**，印之前过 RedactProxy），由调用方按自己记下的测速结果去换算。
type Exit struct {
	mu       sync.Mutex
	set      bool
	proxy    string
	identity string
}

type exitKey struct{}

// WithExit 在 ctx 上挂一个 Exit，用这个 ctx 发出的请求会把出口写进去。
//
// 一个 ctx 一个：两次请求共用同一个 ctx 时，后发的覆盖先发的。要分开记就各挂一个
// （子 ctx 上再调一次 WithExit，内层的遮住外层的）。
func WithExit(ctx context.Context) (context.Context, *Exit) {
	e := &Exit{}
	return context.WithValue(ctx, exitKey{}, e), e
}

// Get 返回代理 URL 与是否直连。ok 为 false = 这个 ctx 上还没有请求真的发出去过
// （身份池建不起来、等位置时 ctx 就到期了、或者调用方根本没发请求）。
func (e *Exit) Get() (proxyURL string, direct bool, ok bool) {
	if e == nil {
		return "", false, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.proxy, e.set && e.proxy == "", e.set
}

// Identity 返回那个身份的标签（"chrome_133/win via socks5://…"，口令已打码），
// 给日志用。没有请求发出去过时是空串。
func (e *Exit) Identity() string {
	if e == nil {
		return ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.identity
}

// recordExit 把 p 记成 ctx 上那个 Exit 的出口。ctx 上没挂 Exit 时什么都不做。
func recordExit(ctx context.Context, p *persona) {
	e, _ := ctx.Value(exitKey{}).(*Exit)
	if e == nil || p == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.set = true
	e.identity = p.label
	e.proxy = ""
	if p.out != nil {
		e.proxy = p.out.url
	}
}
