# HTTP 出口：静态文件、压缩、WriteHeader、WebSocket

## The framework never serves the working directory

There used to be a `UseRootStatics` option, defaulting to **true**, that registered
`GET /<:fileName>.<:ext>` onto a handler which passed the URL to `http.ServeFile` as a path
**relative to the process working directory**. Any `.html` / `.txt` / `.xml` sitting next to
the binary was reachable.

It never fired, purely by luck: the route tree's placeholder syntax is `{:name}` and that
line used `<:name>`, so the whole pattern was registered as a *literal* and only ever matched
the string `/<:fileName>.<:ext>`; on top of that, the old `config.Register` ran `Load()` while
`cfg.Router` was still nil inside `New()`, so usually nothing was registered at all. Rewriting
the syntax "correctly" would have handed every process a working-directory file server —
measured: `GET /secret.xml` returns 200 with the file's contents.

The whole thing is gone, together with `StaticDir` and `StaticExt`, the two allowlist fields
that were meant to fence it in and had no reader anywhere. Downstream already serves
`/robots.txt` and `/favicon.ico` from explicit module routes.
`TestRouter_DoesNotServeTheWorkingDirectory` pins the property, not the implementation:
anyone adding root-level static serving has to turn it red first.

## Static files: modtime is what buys the 304

`TStaticStore` (`router/static.go`) is a TTL memory cache in front of a disk dir and an
`embed.FS`. Two things it got wrong, both invisible until measured:

- **`memFileInfo.ModTime()` returned the zero time.** `http.ServeContent` only emits
  `Last-Modified`, and only honours `If-Modified-Since`, when modtime is non-zero — so static
  assets had no conditional caching at all, and every browser cache expiry re-downloaded the
  whole body. Disk files now carry the file's real mtime; `embed.FS` has none, so it borrows
  the **executable's** mtime, which is exactly the right version key (embedded assets ship
  with the binary). The modtime is cached *alongside* the bytes — cache only the `[]byte` and
  the hit path has no modtime, which reads as "the first request can 304 but later ones
  cannot", harder to diagnose than never working.
- **Nothing capped what went into memory.** Every served file was slurped whole and kept for
  the TTL, and `StaticCacheTTL: 0` means never expire. One large file in a static dir became
  resident forever. Files over `DefaultStaticCacheMaxFileSize` (4MiB) now stream straight from
  disk or embed and are not cached; `*os.File` and embed files are both seekable, so Range
  and 304 still work.

`Cache-Control: public, max-age=3600` is still set, so the 304 only happens after that hour
elapses; the two together are what make a repeat visit cheap.

A name containing `..` is rejected outright, even for a legitimate file called `..hidden`.
`path.Clean("/"+name)` already makes traversal impossible, so that check is a second line of
defence, and keeping it costs only an exotic filename. Worth knowing because it surfaces as
"the file is right there in the directory but 404s".

## Response compression

`router.WithCompress()` turns on gzip/deflate on the HTTP egress (`router/compress.go`).
Measured on the real front end: vertex's main bundle 2.06MB → 589KB (71%), a lazy chunk
74%, `website.css` 82%. Before this, every page and every `call_kw` went out at full size,
and `connect_components` had to hand-roll its own `serveBytes` to re-compress what it
proxied.

Four rules that are easy to get wrong:

- **It is off by default, and that is a security decision, not caution.** Compressing an
  attacker-influenced input together with a secret in the same response (a CSRF token, a
  session id) makes the compression ratio a side channel — that is BREACH, and HTTPS does
  not stop it because what leaks is ciphertext *length*. Turning it on is the deployer's
  explicit act.
- **It is not a middleware, and cannot be one.** A middleware ends when `ctx.Next()`
  returns, but the body is written *afterwards* by `ctx.Apply()` — closing the compression
  stream at the end of `Next` closes a stream that has not yet seen a single byte. The
  only place that knows when a response ends is `TRouter.ServeHTTP`, so the wrapper is
  installed and closed there. Disabled, the cost is one nil check per request.
- **The decision is deferred, because size is not known at `WriteHeader` time.**
  `Content-Length` is usually absent, so the writer buffers up to `minSize` (default 1KB)
  and only then commits to compressing. `Flush` forces the decision — a streaming first
  frame (SSE `: connected`) is far under the threshold and must go out *now*, not sit in a
  buffer waiting for a threshold that will never arrive. Regression:
  `TestCompress_StreamingFlushIsNotStalled` (it hangs, not fails, when this is wrong).
- **Compressing rewrites the entity, so three headers must follow.** `Content-Length` is
  deleted (net/http recomputes it — leave the original and the client waits forever for
  bytes that never come), a strong `ETag` is weakened to `W/`, and `Vary: Accept-Encoding`
  goes out on **every** response, compressed or not, or a shared cache will hand the
  compressed body to a client that cannot decode it.

Never compressed: `Content-Encoding` already set (a reverse-proxied response the upstream
already compressed — compressing gzip again costs CPU and yields something no client can
read), 204/304/206 and any `Content-Range`, non-2xx, HEAD, upgrade requests, and content
types outside `DefaultCompressTypes`. `q=0` in `Accept-Encoding` is an explicit refusal and
is honoured — answering with an encoding the client just declined renders the page as
binary garbage.

## WriteHeader is deferred, and 204 / 304 must stay empty

`THttpResponse.WriteHeader` only *records* the status; the status line goes out on the first
`Write` / `Flush`, or at `Apply`'s `Commit()`. That is what lets `ctx.WriteHeader(422)` be
followed by `ctx.RespondByJson(x)` — under immediate commit the headers froze first and
`Apply` dropped the body, so every non-200 JSON response went out empty.

Deferring it opened one hole that has to be closed alongside: `Apply` now judges on "has a
body been written", so `ctx.NotModified(); ctx.Respond(x)` sends `x` where the old `Written()`
check silently swallowed it. `THttpResponse.Write` therefore refuses a body on 204 / 304 / 1xx
(`http.ErrBodyNotAllowed`, same rule and same error as net/http). See Testing Notes for why
that guard can only be tested on a recorder, never on a live server.

## 池化的 context：每个字段都要在 reset 里有归宿

`THttpContext` 按**路由**走 `httpCtxPool` 复用（`TRpcContext` 同理走 `rpcCtxPool`）。
`reset` 里漏掉任何一个字段，那个字段就活到下一个复用同一路由的请求里。已经因此出过的：

- `pathPrefix` —— 上一轮带 `/fr` 前缀这一轮不带，链接生成与重定向凭空多出一门语言；
- `hijacked` —— 上一轮被 WebSocket 接管过；
- **`context`** —— 2026-09-24 修。`self.context` 里挂着上一个请求往里塞的**任何值**。
  `TRpcContext.reset` 一直清着并在注释里写明了危害，HTTP 这一侧是漏的。

最后一条的后果不是"看到别人的调试值"那么轻。vectors 在 context 上挂的是
`"AuthSession"`（整条会话）、调用方身份、租户作用域，而**鉴权中间件在认之前就拒掉的
那一支**（Token 为空 / 验签失败 → 401）在 `return` 之前不会挂 AuthSession —— 于是写响应
那一路读到的是上一位已登录用户的会话。真栈量到的形状：一个完全不带 `Auth` 头的
`POST /api/session/set_debug` 拿到了只该给管理员的错误诊断附件。顺着同一条路还有
vectors 的 `module.InitModelController` 与 `core/model.TModel.GetSession()`，两者都按
`ctx.Context().Value("AuthSession")` 取身份 —— 那就从"看到"升级成"顶着别人的身份执行"。

归宿是 `nil`：`Context()` 取到 nil 时自己回落 `context.Background()`，所以清掉不会让
任何调用方拿到 nil。回归测试 `router/http_context_pool_leak_test.go`。

**加字段就加 reset**。这一类缺陷没有任何报告：它只在同一条路由上连着来两个请求、且前一个
恰好塞过值时才出现，看起来是"偶发"。

## WebSocket

Native server-side WebSocket (RFC 6455) lives in two layers: `transport/websocket.go` is
the frame codec and connection (`TWebsocketConn`), `router/websocket.go` is the handshake
(`TWebsocketUpgrader`, plus `THttpContext.Upgrade()` / `IsWebsocket()`). There is no
third-party dependency — not gorilla, not nhooyr.

```go
grp.Url([]string{"GET"}, "/bus/websocket", func(ctx *router.THttpContext) {
    ws, err := ctx.Upgrade()   // 或 (&router.TWebsocketUpgrader{...}).Upgrade(ctx)
    if err != nil { return }   // Upgrade 已写好错误响应
    go serve(ws)               // 连接生命周期归调用方，用完必须 Close
})
```

Four rules that are easy to get wrong:

- **A successful upgrade detaches the context from the pool.** `Upgrade` sets
  `ctx.hijacked`, and `TRouter.ServeHTTP` returns immediately instead of running
  `ctx.Apply()` / `pool.Put(ctx)` / `respPool.Put(rsp)`. Skipping this is not cosmetic: the
  WS goroutine still holds `ctx` and `rsp`, so recycling hands the *same* objects to the
  next request, and the pool-return path nils `rsp.ResponseWriter` out from under the live
  connection. `THttpContext.reset` must keep clearing the flag, or a recycled context makes
  an ordinary request skip `Apply` and return an empty body.
  Regression: `TestWebsocketUpgrade_ContextNotReturnedToPool`,
  `TestWebsocketUpgrade_RouterDoesNotApplyAfterHijack`.
- **`CheckOrigin: nil` means same-origin, not allow-all.** Browsers apply no same-origin
  policy to WebSocket and send no preflight — the handshake is a plain GET any page can
  issue. Allowing every `Origin` hands the endpoint to arbitrary third-party pages (CSWSH).
  Cross-origin needs an explicit allowlist.
- **The read limit is load-bearing.** The 127-length branch takes a peer-supplied `uint64`
  and would otherwise drive a `make([]byte, n)` — same memory-amplification shape as the
  gob restriction above. Default is 32MB, enforced *twice*: once per frame, and again when
  merging fragments (each fragment can be small while the total is not).
- **One reader goroutine, many writers.** `ReadMessage` is not safe to call concurrently;
  `WriteMessage`/`WriteControl` are (they hold `wmu`), which is what lets a broadcast fan
  out to many connections.

`transport/websocket_fuzz_test.go` holds the fuzz targets (`FuzzWebsocketFrameDecode`,
`FuzzWebsocketRoundTrip`).

Note this is unrelated to `router/reverse_proxy.go`'s WebSocket handling — that one only
passes an `Upgrade` request through to an upstream and never parses a frame.
