package router

import (
	"strings"
)

/*
同进程改派（forward）：把**正在处理中的**请求交给本路由树上的另一条路径。

# 这是给谁用的

vectors 的「站点应用挂载」：站点管理员把网店从 `/shop` 改名成 `/store` 之后，
`/store/cart` 这条地址在路由树上**根本不存在** —— 前缀是某个租户的某个站点的数据，
而路由树是装配期建的、一个进程服务所有租户。它落到根级通配 `/*` 上，由前台控制器
查出「/store 挂着 website_sale.shop」，再把这次请求交还给 `/shop/cart` 那条**真路由**。

# 为什么不是"再调一次 ServeHTTP"

把 `r.URL.Path` 改掉、拿同一个 `http.ResponseWriter` 重新进一次 `ServeHTTP` 看着更省事，
但那样一次请求里会有**两个** context 和两个 `THttpResponse`：内层把响应写完，外层回到
`ServeHTTP` 末尾照样走一遍 `ctx.Apply()` —— 它见自己这份 response 没写过东西，就补一次
`WriteHeader(200)`，于是每一个挂载请求在 net/http 那里记一条
`superfluous response.WriteHeader call`。响应本身没坏，日志被刷烂了。
（`forward_test.go` 里 `TestForwardDoesNotLogSuperfluousWriteHeader` 盯着这件事。）

所以改派复用**同一个 context**：换掉 route、路径参数与 `URL.Path`，重跑目标路由的
handler 链。这样也顺带保住了三样东西 —— 会话与调用方身份（都挂在 `ctx.context` 上，
外层中间件已经解好）、同一份响应对象（`Respond` 写进 `Result`，由外层那一次 `Apply`
统一发出）、以及 `Recover`（`router.route` 自带）。

# 行为与直接打目标地址一致

参数取自**树的匹配结果**，不是调用方算出来的：`/shop/page/{page}` 上 `page` 是什么，
改派过来就是什么。目标路由的中间件链也照跑（鉴权、会话、租户作用域都在那上面）。
换句话说 `Forward("/shop/cart")` 与访客直接请求 `/shop/cart` 走的是同一批代码。

# 调用方要知道的

  - 返回 false = **一个字节都没写**，树上没有这条路径（或方法不对）。调用方自己决定
    回 404 / 405 / 还是继续往下找。返回 true = 目标已经处理完，直接 return。
  - `URL.Path` 在改派期间是目标路径，改派返回后恢复原值 —— 访问日志与外层中间件
    看到的仍是访客请求的那条地址。对外地址要交给 handler 的话走请求头
    （vectors 用 `X-Vs-Mount-Prefix` / `X-Vs-Public-Path`）。
  - 不做尾斜杠归一：`ServeHTTP` 那一层的 301 是按**访客地址**跳的，改派的目标是内部
    地址，跳过去等于把内部地址暴露到浏览器地址栏。调用方要自己补（判据同树侧：
    原样匹配不到、去掉斜杠匹配得到才跳，跳的是对外地址）。
  - 改派到自己那条路由会被直接拒（无限递归），链式改派最多 maxForwardDepth 层。
*/

// maxForwardDepth 是一次请求里允许的改派层数。正常用法是一层（通配 → 真路由）；
// 留几层给"应用再改派给应用"的情形，够不上就是环，宁可断在这里。
const maxForwardDepth = 4

// Forward 把本次请求改派给 path 对应的路由并执行它。
//
// 返回 true 表示目标路由已经跑完（响应归它），调用方应当立即返回；
// 返回 false 表示树上没有这条路径，**没有任何东西被写出**。
func (self *THttpContext) Forward(path string) bool {
	if self.router == nil || self.request == nil || self.request.Request == nil {
		return false
	}
	if !strings.HasPrefix(path, "/") {
		log.Errf("forward: 目标路径 %q 不是以 / 开头的绝对路径", path)
		return false
	}
	if self.forwardDepth >= maxForwardDepth {
		log.Errf("forward: 改派层数超过 %d（目标 %s），当成环断开", maxForwardDepth, path)
		return false
	}

	route, params := self.router.tree.Match(self.request.Method, path)
	if route == nil {
		return false
	}
	// 改派回自己这条路由 = 无限递归。判 Id 而不是判路径：通配路由上原路径与目标
	// 路径本来就不相等，真正的判据是"还是这条路由在处理"。
	if self.route != nil && route.Id() == self.route.Id() {
		log.Errf("forward: %s 改派到的还是当前这条路由（%s），拒绝 —— 那是一个环",
			path, self.route.Path())
		return false
	}

	prevRoute := self.route
	prevHandler := self.handler
	prevIndex := self.handlerIndex
	prevParams := self.pathParams
	prevPath := self.request.URL.Path

	self.route = route
	self.handlerIndex = 0
	// 参数必须整套换掉，不能只覆盖同名的那几个：来路是通配路由 `/*`，它往参数集里
	// 塞过一个"余下的全部路径"，留着就串进目标 handler 了。
	self.pathParams = NewParamsSet(self)
	self.setPathParams(params)
	self.request.URL.Path = path
	self.forwardDepth++

	self.router.route(route, self)

	self.forwardDepth--
	self.request.URL.Path = prevPath
	self.pathParams = prevParams
	self.handlerIndex = prevIndex
	self.handler = prevHandler
	self.route = prevRoute
	// isDone / Result / 响应状态**不还原**：目标已经决定了这次请求怎么回答。
	return true
}
