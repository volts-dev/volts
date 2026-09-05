package transport

import "net/http"

// applyServerLimits 把连接级的时限与上限统一施加到 http.Server 上。
//
// 收在一处，是因为本包有**两个**建 http.Server 的地方（httpTransportListener 与
// transportListener），而它们此前给的不是一套东西：前者设了读写超时与 8KiB 头部上限，
// 后者一个都没设 —— 走到后者的部署里，一条连接可以永远不把请求头发完。两份各写一遍，
// 下一次加一项限制时必然又只加一处。
//
// 四项各管一段，缺一段就等于那一段没有上限：
//
//	握手后 → 请求头读完   ReadHeaderTimeout   （Slowloris 打的就是这一段）
//	请求头 → 请求体读完   ReadTimeout         （大上传需要它长）
//	开始写 → 响应写完     WriteTimeout        （SSE/长响应需要它长，或按连接改）
//	上一个请求 → 下一个   IdleTimeout         （keep-alive 空闲）
//
// ★ ReadHeaderTimeout 与 IdleTimeout 不设时，标准库回落到 ReadTimeout。也就是说
// **凡是把 ReadTimeout 调大的部署，都顺带把这两段一起放开了** —— 包括 Debug()：
// 它把 ReadTimeout 设成 999s 是为了让调试器能在断点上停住，而断点停在 handler 里，
// 那已经是请求头读完之后的事。所以这两项在这里独立取值，Debug 动不到。
func applyServerLimits(srv *http.Server, cfg *Config) {
	if srv == nil || cfg == nil {
		return
	}
	srv.ReadTimeout = cfg.ReadTimeout
	srv.WriteTimeout = cfg.WriteTimeout

	// <=0 视为"没配"，用安全默认而不是"不限"：这两项一旦不限就回落到 ReadTimeout，
	// 正是本函数要避免的那件事。真要不限的人可以把 ReadTimeout 一起调大。
	if cfg.ReadHeaderTimeout > 0 {
		srv.ReadHeaderTimeout = cfg.ReadHeaderTimeout
	} else {
		srv.ReadHeaderTimeout = DefaultReadHeaderTimeout
	}
	if cfg.IdleTimeout > 0 {
		srv.IdleTimeout = cfg.IdleTimeout
	} else {
		srv.IdleTimeout = DefaultIdleTimeout
	}
	if cfg.MaxHeaderBytes > 0 {
		srv.MaxHeaderBytes = cfg.MaxHeaderBytes
	} else {
		srv.MaxHeaderBytes = DefaultMaxHeaderBytes
	}
}
