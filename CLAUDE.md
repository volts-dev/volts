# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# Build
go build ./...

# Run all tests
go test ./...

# Run tests in a specific package
go test ./router/...
go test ./transport/...

# Run a single test
go test ./router/ -run TestTree
go test ./transport/ -run TestTCPTransport

# Run benchmarks
go test ./router/ -bench=. -benchmem

# Run tests with race detector
go test -race ./...

# Vet
go vet ./...
```

## Architecture

Volts is a Go web+RPC framework with a layered, interface-driven architecture. The key abstraction layers from bottom to top:

```
IService (volts.go)
  └─ IServer (server/)
       └─ IRouter (router/)
            └─ IContext (router/context.go) — THttpContext or TRpcContext
  └─ ITransport (transport/) — HTTP or TCP wire protocol
  └─ IRegistry (registry/) — service discovery backend
  └─ IBroker (broker/) — pub/sub messaging
  └─ IClient (client/) — outbound RPC/HTTP calls
```

### Request Flow

1. `ITransport` accepts a connection and hands it to `IServer`
2. `IServer` passes the request to `IRouter`
3. `IRouter` matches the URL/method against the radix tree (`router/tree.go`)
4. The matched handler chain (middleware + handler) executes with a pooled context (`THttpContext` or `TRpcContext`)
5. The context writes the response back through the transport

### Key Design Conventions

**Options pattern** — all configuration uses `func(*Config)` closures. Every package exposes `New(opts ...Option)` and a `Default()` singleton.

**`String()` method** — every object must implement `String()` returning its name (see doc.go).

**Context pooling** — `router/router.go` maintains per-route `sync.Pool` maps (`httpCtxPool`, `rpcCtxPool`) keyed by route pattern to avoid allocations on the hot path.

**Handler registration** — `router/handler.go` uses reflection to resolve methods at registration time. HTTP handlers receive `*THttpContext`; RPC handlers follow the `func(ctx, req, rsp) error` signature. Route method `"CONNECT"` signals an RPC endpoint.

**Codec dispatch** — `codec/codec.go` registers codecs by content-type hash. The transport `message.go` carries a codec identifier in the wire frame; the server/client negotiate serialization format (JSON via the standard library, MessagePack, Protobuf, Gob, or raw bytes).

The serialize type is **one attacker-controlled byte on the wire** (HTTP side: `Content-Type`), so parsing peer input must go through `codec.IdentifyWireCodec`, not `IdentifyCodec`. Gob is registered via `RegisterRestrictedCodec` and is therefore *not* wire-selectable by default (`encoding/gob` on untrusted input is a memory-amplification vector); opt in with `codec.SetWireAllowed("Gob", true)`.

`SerializeType` is a `byte` and `HashName` truncates crc32 to its low 8 bits — only 256 ids exist. `RegisterCodec` panics on collision rather than silently overwriting. Built-in ids are part of the wire protocol and pinned by `codec/safety_test.go`.

**Middleware assembly** — three mechanisms, all resolved at assembly time, never per-request:

1. **Global chain** — `router.Use(...)`, applies to every route including ones added later.
2. **Route-level** — `Url(methods, path, mainCtrl, mid1, mid2)`; `handlers[0]` is the controller, `handlers[1:]` are middlewares. Getting this order backwards reports `reflect: call of reflect.Value.NumField on interface Value`, which names nothing relevant.
3. **Controller embedding (DI)** — `RegisterMiddleware` only records a constructor by name; binding happens by embedding the middleware type in the controller struct. `handler.init` asserts against the controller **value**, so middleware methods must use **value receivers** — pointer receivers silently fail to bind.

**Value receivers are a router-wide rule, not just a middleware one.** Registering a
struct controller (`Url(methods, path, ctrl)`) dereferences it first —
`if kind == reflect.Ptr { ctrlType = ctrlType.Elem() }` in `router/group.go` — and then
walks `ctrlType.NumMethod()`. A value type's method set excludes pointer-receiver
methods, so `func (c *Ctrl) Foo(ctx)` is **never registered**. Callers see
`rpc status NotFound: server returned no detail` — a message that names neither the
controller nor the method.

Execution order: global → route-level → embedded → main controller.

Because the framework serves HTTP and RPC from separate handler objects (the handler id includes `TransportType`), `Use` filters by transport **at assembly time**: the argument type carries the intent (`func(*THttpContext)` / `func(*TRpcContext)` / `func(IContext)`), and an `IMiddleware` may declare scope via the optional `IMiddlewareTransport`. Entries that do not apply are absent from the other side's `funcs` slice, so the cost is literally zero rather than merely small. **Any new cross-cutting feature must follow this rule — branch at registration, never in the request path.**

### The framework never serves the working directory

There used to be a `UseRootStatics` option, defaulting to **true**, that registered
`GET /<:fileName>.<:ext>` onto a handler which passed the URL to `http.ServeFile` as a path
**relative to the process working directory**. Any `.html` / `.txt` / `.xml` sitting next to
the binary was reachable.

It never fired, purely by luck: the route tree's placeholder syntax is `{:name}` and that
line used `<:name>`, so the whole pattern was registered as a *literal* and only ever matched
the string `/<:fileName>.<:ext>`; on top of that, `config.Register` runs `Load()` while
`cfg.Router` is still nil inside `New()`, so usually nothing was registered at all. Rewriting
the syntax "correctly" would have handed every process a working-directory file server —
measured: `GET /secret.xml` returns 200 with the file's contents.

The whole thing is gone, together with `StaticDir` and `StaticExt`, the two allowlist fields
that were meant to fence it in and had no reader anywhere. Downstream already serves
`/robots.txt` and `/favicon.ico` from explicit module routes.
`TestRouter_DoesNotServeTheWorkingDirectory` pins the property, not the implementation:
anyone adding root-level static serving has to turn it red first.

### Static files: modtime is what buys the 304

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

### Options that set a field nobody reads

A recurring shape in this repo, and the reason two batch scans exist: a `WithX()` writes a
struct field, the field has **zero readers**, and the caller gets no diagnostic. Six were
removed at once — `router.WithIgnoreCase`, `router.WithStaticHandler`,
`client.AllowRedirect`, `client.WithStreamTimeout`, `logger.WithWrite`, and the whole
`internal/acme/options.go` (its `Provider` interface takes no options; the autocert
implementation says so in its own package comment).

Two of them are worth knowing individually:

- **`WithIgnoreCase` was applied by default and then ignored.** `NewGroup` built every
  group's tree with it, so the framework declared case-insensitive routing and never
  implemented it. It was deleted rather than implemented: turning it on would change the
  match set of every route already in production, `/{module}/static/...` resolves onto a
  case-sensitive filesystem (so "route matched, file missing" becomes possible), and
  case-insensitive path matching is a classic ACL-bypass shape — the middleware checks one
  casing, the backend opens another. `router/case_sensitive_routing_test.go` pins the
  property.
- **`server.AutoCreate` is kept, marked `Deprecated`.** It has no reader either, but
  vectors' `service.go` calls it twice, so removing it is a coordinated up/downstream
  change rather than a cleanup.

Separately, `HttpTransport.Listen`'s ACME branch wrote `config, err := ...`, which shadowed
the outer `err`; the later `l, err = vnet.Listen(...)` then assigned to the **inner** one,
so the function's own error check could never fire. An ACME listen failure returned a
listener wrapping `nil` and the server reported a successful start while listening on
nothing. `go vet` does not catch this — the `shadow` analyzer
(`go vet -vettool=$(go env GOPATH)/bin/shadow ./...`) does, and the rest of what it reports
here is benign inner scoping.

### Connection limits: four segments, and `Debug()` may only widen two

`transport.applyServerLimits` is the single place that configures every `http.Server` this
package builds (there are two construction sites, and they used to disagree —
`transport/listener.go` set no timeout and no header cap at all). Four settings each bound a
different segment of a connection's life, and a segment left unbounded is unbounded:

| segment | setting |
|---|---|
| handshake → request headers read | `ReadHeaderTimeout` (20s) |
| headers → request body read | `ReadTimeout` (15s) |
| first write → response written | `WriteTimeout` (15s) |
| between keep-alive requests | `IdleTimeout` (120s) |

**`ReadHeaderTimeout` and `IdleTimeout` fall back to `ReadTimeout` when unset**, so every
deployment that raises `ReadTimeout` for a legitimate reason silently opens those two as
well. `transport.Debug()` raises it to 999s — and vectors' `cmd.Start()` calls
`volts.Debug()` **unconditionally** for both servers (measured: read/write/dial all become
16m39s). That left every process in production willing to spend sixteen minutes receiving a
few hundred bytes of request header, which is precisely the Slowloris precondition. So these
two are configured independently and `Debug()` does not touch them: a debugger breakpoint
sits inside a handler, which is *after* the headers are read.

Regression: `transport/server_limits_test.go`. `TestSlowHeaderConnectionIsClosed` is
behavioural, not a field assertion — it dribbles half a request line into a real socket and
asserts the server gives up on schedule (drop the setting and it hangs for the full
`ReadTimeout`). Note the server answers a header timeout with `400` + `Connection: close`
before closing, so the judgement is *when* it gives up, not what it says.

### Response compression

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

### WriteHeader is deferred, and 204 / 304 must stay empty

`THttpResponse.WriteHeader` only *records* the status; the status line goes out on the first
`Write` / `Flush`, or at `Apply`'s `Commit()`. That is what lets `ctx.WriteHeader(422)` be
followed by `ctx.RespondByJson(x)` — under immediate commit the headers froze first and
`Apply` dropped the body, so every non-200 JSON response went out empty.

Deferring it opened one hole that has to be closed alongside: `Apply` now judges on "has a
body been written", so `ctx.NotModified(); ctx.Respond(x)` sends `x` where the old `Written()`
check silently swallowed it. `THttpResponse.Write` therefore refuses a body on 204 / 304 / 1xx
(`http.ErrBodyNotAllowed`, same rule and same error as net/http). See Testing Notes for why
that guard can only be tested on a recorder, never on a live server.

### WebSocket

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

### Package Responsibilities

| Package | Responsibility |
|---|---|
| `router/` | Radix-tree routing, handler reflection, middleware pipeline, HTTP/RPC context, reverse proxy, static files |
| `server/` | Lifecycle management (Start/Stop), registry integration, broker subscriptions, transport wiring |
| `client/` | Outbound HTTP and RPC calls, service discovery via selector, retry logic |
| `transport/` | Wire-level HTTP and TCP transports, TLS/ACME, message framing with CRC32, proxy dialers (HTTP CONNECT / SOCKS5 / Shadowsocks / ShadowsocksR) |
| `codec/` | Serialization adapters (JSON via stdlib encoding/json, MsgPack, Protobuf, Gob, bytes) |
| `registry/` | Service registration/discovery — Consul, ETCD, mDNS, or in-memory backends |
| `broker/` | Pub/sub messaging — HTTP or in-memory backends; `broker/redis` (opt-in blank import) adds a Redis Streams backend with real acks and redelivery |
| `selector/` | Load-balancing strategy over registry nodes, plus per-node failure ejection / circuit breaking (`selector/breaker.go`) |
| `logger/` | Self-contained leveled logger (writer manager + console writer). **Not** built on zap — `go.uber.org/zap` is pulled in only by `registry/etcd` for clientv3's `LogConfig` |
| `internal/` | Shared utilities (pool, metadata, TLS helpers, mDNS, ACME, backoff) |
| `core/outbound/` | Outbound HTTP for scraping **third-party** sites — browser identity rotation (TLS fingerprint + UA + client hints + proxy exit). See "Outbound: talking to strangers" |
| `config/` | Config models, load/validate/lint/report. viper is the file codec only — layering, provenance and diff-persistence live in this package (see "Config: the file is a read-only input") |

### Service Lifecycle

```go
app := volts.New(
    volts.Server(srv),          // required
    volts.Transport(...),       // optional, defaults to HTTP
    volts.Registry(...),        // optional
    volts.BeforeStart(fn),      // lifecycle hooks
    volts.AfterStop(fn),
)
app.Run()  // blocks until SIGTERM/SIGINT/SIGQUIT or context cancel
```

`Run()` → `Start()` → signals server.Start() → listens on transport → waits for OS signal → `Stop()`.

### Address Configuration

`server.Address(":PORT")` must be used to bind all interfaces. `localhost:PORT` and `127.0.0.1:PORT` are automatically normalized to `:PORT` by `internal/net.Listen()` via `normalizeBindAddr()`.

`server.Address()` sets both `cfg.Address` and `cfg.Transport.Config().Addrs`. When `Address()` is called before Transport is initialized (nil), `newConfig()` compensates by syncing `cfg.Address` to the Transport after creation.

### LocalFormat and macOS Networking

`internal/addr.LocalFormat()` converts local machine IPs to loopback (`127.0.0.1`) before outbound calls in `client/rpc_client.go`, `client/http_client.go`, and `router/reverse_proxy.go`. This is intentional — macOS network extensions (VPN, firewalls) intercept traffic on physical interfaces but not loopback. Do not remove these calls.

On macOS, browser/curl access via LAN IP to a locally-running server is intercepted at the kernel level. Use `localhost` for local development access instead.

### HttpClient could not do HTTPS at all

`client.HttpClient`'s HTTPS path ended in a **panic on the caller's goroutine**, not an
error. `roundTripper.dialTLS` only registered a transport when the connection satisfied
`transport.INegotiatedProtocol` — which `core/ja3`'s utls wrapper does and **`*tls.Conn`
does not** (crypto/tls exposes ALPN through `ConnectionState()`, not a method of that
name). With no fingerprint configured nothing was ever written to `cachedTransports`, and
`RoundTrip` then called a method on a nil interface. vectors only uses this client for
plain intra-cluster HTTP, so it never fired there — but turning on TLS between services
would have taken down every caller at once.

Three things had to move together, and the second only becomes reachable once the first
is fixed:

- **Read ALPN by capability, not by one vendor's type.** `transport.NegotiatedProtocolOf`
  accepts both shapes. On top of that `dialTLS` now falls back to HTTP/1.1 when the
  protocol cannot be read at all — an unknown ALPN must land on a working default, never
  on "register nothing". `RoundTrip` returns a readable error if the map is still empty,
  because a library must not panic in its caller's goroutine.
- **A configured proxy was silently bypassed.** `DialConfig.ProxyURL` had **no reader
  anywhere**, and `dialCfg.dialer` was consulted only inside `HttpTransport.Dial`'s JA3
  branch — plain TLS went out through `tls.DialWithDialer`, i.e. direct. So "proxy
  configured, no fingerprint" put the request on the host's own IP while the caller
  believed otherwise, which is the exact failure `core/outbound`'s `allow_direct=false`
  exists to prevent. Every branch now dials through one `rawDial`, which prefers
  `WithDialer`, then builds one from `WithProxyURL`, and otherwise keeps the old
  env-proxy-aware `newConn` wrapper. `tls.Client` needs `ServerName` filled in by hand
  (`tls.Dial` infers it; `tls.Client` does not) and a handshake deadline, or a peer that
  completes TCP and never sends ServerHello holds the caller forever.
- **`User-Agent` was wiped on every request.** `call()` computes it from
  `config.UserAgent` / `Ja3.UserAgent`; `RoundTrip` then did an unconditional
  `Set("User-Agent", rt.UserAgent)` against a field the constructor never filled. Every
  request went out with an empty UA and the whole `WithUserAgent` / JA3-UA pairing was
  dead. It is now only applied when non-empty.

Two smaller ones in the same path: `RoundTrip` did all its map work — nil check, make,
read, write — outside the lock that `dialTLS` holds, so concurrent requests raced (and
`concurrent map writes` is a crash, not a warning); and the per-dial option slice was
`append`ed to **in place** inside the captured closure, growing by three entries per dial
without bound and racing on the same slice variable. `cfg.DialOptions` — written by
`Init`, read by nobody — is now the single source both the constructor and `Init` seed, so
`Init(WithJa3(...))` / `Init(WithProxyURL(...))` finally take effect instead of silently
doing nothing.

Regression: `client/https_dial_test.go`. `TestProxy_IsUsedForHTTPSWithoutFingerprint`
stands up a real CONNECT proxy and counts hits — asserting on the dial helper instead
passes even when the plain-TLS branch dials direct, because the raw dial has already gone
through the proxy by then.

### A client response decodes with the *response's* codec

`httpResponse` used to build its body with the **request's** codec
(`body.New(req.body.Codec)`). For a POST that sends JSON and receives JSON that happens to be
right; for a GET it is not — a request with no body gets the bytes codec, so
`rsp.Body().Decode(&out)` fails with `... is not a []byte`, which reads as "the peer returned
the wrong format" rather than "we picked the wrong decoder". vectors' `core/codec` takes
exactly this path. The codec now comes from the response's `Content-Type`, falling back to the
request's when it is absent or unrecognised.

**Use `codec.IdentifyWireCodec`, never `IdentifyCodec`, for that lookup.** `Content-Type` is
filled in by the peer, so it decides which decoder we point at the peer's own bytes — the same
rule as the RPC header byte, and the reason Gob is not wire-selectable.
`TestResponseCodec_PicksByResponseContentType` pins it, including that a response claiming
`gob` gets the fallback.

While there: `httpResponse.Read(out)` was a stub whose whole body was `return nil` — the
caller got an untouched zero value and a nil error. It is not in `IResponse` and had no
callers, but `HttpResponse` is an exported alias, so it was reachable. It now decodes.
`Body()` also dropped a dead re-read of `self.response.Body`, which by then is already closed
and drained; its error check was inverted (`if err == nil { log.Err(err) }`), logging a nil on
success and staying silent on real failures.

### Errors carry a code, and the code must survive wrapping

`errors.Error` is the framework's error type: `Id`, `Code`, `Detail`, `Status`. Upper layers
(vectors' `codec.WriteData`) read `Code` to decide what status the caller sees, and anything
they cannot recognise is flattened to 500.

- **Read the code with `errors.Code(err)` / `errors.From(err)`, never a type assertion.**
  Business code wraps freely (`fmt.Errorf("...: %w", err)`), and `err.(*errors.Error)` misses
  the moment anything wraps it — the caller then gets a 500 for what was a 404. `Code` walks
  the chain.
- **`errors.Wrap(err, id, code, detail)` keeps the cause reachable**, so `errors.Is` can still
  identify a sentinel underneath. The cause is unexported: it is *not* serialized, so what
  crosses a process boundary is still only the four fields.
- **`(*Error).Is` compares Id + Code + Detail**, so a sentinel survives a JSON round-trip
  through RPC — pointer equality does not, and `Parse` builds a new object every time. All
  three fields matter: `PermissionDenied` and `UnavailableService` share Id `""` and Code 500
  and differ only in Detail.
- The package re-exports stdlib `Is` / `As` / `Join` / `Unwrap`, so a file that imports
  `volts/errors` does not also need the stdlib one under an alias.

### Retries are off by default, and that is deliberate

`client.DefaultRetry` is `RetryNever`. Retrying a non-idempotent call means executing it
twice, and this stack carries sale / purchase / account / payment — the same reasoning as the
reverse proxy's refusal to replay POSTs. Opting in takes two knobs, both of which are now
real: `client.WithRetries(n)` (the attempt count — `Config.Retries`, i.e. `client.*.retries`
in config.json, had **no reader at all** and was a knob that did nothing) and
`client.WithRetry(fn)` (the policy).

- **`RetryOnConnectError` is the only provably safe policy.** It fires solely on
  `client.ErrConnectFailed` — the connection was never established, so not one byte of the
  request was sent and the server cannot have executed it. A bare `EOF` after the request went
  out is deliberately excluded: "the server never got it" and "the server finished and then
  the connection dropped" are indistinguishable.
- **`RetryOnError` (408/500) is for idempotent reads only.** A 500 means the server *did*
  execute it. It also used to be dead code: it read the status via `errors.Parse(err.Error())`,
  and `Error()` returns only `Detail`, so `Parse` always failed to decode and left `Code` at 0
  — the switch never matched and it returned false for every error, silently.
- The RPC client never consults the policy when the server answered with a non-OK status bit;
  it returns immediately. So on the RPC side the connect-only policy covers every case that is
  retryable at all.

### Node Health / Circuit Breaking

`selector` ejects a node after `BreakerFailures` consecutive failures (default 3), holds it
out for `BreakerCooldown` (default 10s, exponential backoff up to 2min), and fully restores
it on one success. `WithoutBreaker()` disables it.

Two rules that are easy to get wrong:

- **Callers must report both outcomes.** `Selector.Mark(service, node, err)` is the only
  input the breaker has. All three call sites (`client/http_client.go`,
  `client/rpc_client.go`, `router/reverse_proxy.go`) report success *and* failure — an
  earlier version called `Mark` only on the success path, where `err` was always nil, so
  the breaker never saw a single negative signal.
- **Reverse proxy counts connection-level errors only.** A 5xx from upstream means the node
  is reachable; ejecting on it would escalate a business error into lost capacity.

When every node of a service is tripped the filter **fails open** and returns them all: a
breaker that removes 100% of capacity is worse than the failure it guards against.

### Endpoint-aware node selection

A service name can be served by **more than one process**, and those processes do not
necessarily expose the same endpoints: another binary blank-imports the module (and
therefore registers the same service name) while being compiled earlier, or a rolling
upgrade has old and new binaries live at once. `registry.Service` carries `Endpoints`
per **registration entry**, but `etcd.GetService` used to merge entries by `Version`
alone — nodes were all appended while `Endpoints` kept only the *first* entry's. The
node↔endpoint mapping was gone, so `RpcClient` round-robined onto a process that does
not implement the path and got `rpc status NotFound: server returned no detail`, which
is indistinguishable from "this deployment does not have that module" and leaves both
sides' logs clean. Measured on the real stack (2026-09-03, vectors): ~50% of calls to
one endpoint failed, alternating.

Two halves, both required:

- **`registry.EndpointsKey`** — merging is keyed by version **plus** a fingerprint of
  the endpoint set (`GetService` and `registryCacher.update`). Processes with identical
  endpoint sets — one binary's replicas, i.e. the overwhelming majority — still merge
  into a single `Service`, so the shape only changes when it actually has to. Splitting
  costs nothing at balance time: the strategies flatten every `Service`'s nodes before
  picking (`selector/strategy.go`). `ListServices` deliberately does **not** use it —
  that call answers "what services exist", where a duplicated name would only make
  callers double-count.
- **`selector.FilterEndpointIfKnown`** — `RpcClient.next()` now filters candidates by
  the endpoint being called. It **fails open** when nobody advertises that name:
  endpoint naming is the caller's convention (volts itself uses `/model.method`) and
  older registrations may not report endpoints at all, so a strict filter would turn
  "I don't recognise this name" into "the service is unavailable" — the same trade-off
  as the breaker's all-tripped case. The test is "someone advertises it and you don't",
  never "you don't advertise it".

`registryCacher.Register` must keep scanning past entries whose nodes are not ours: a
`(name, version)` can now match several `Service` records, and a sibling process's may
come first — `break`ing on it would register an empty node set as "self", after which
`filteSelf` no longer recognises this process's own nodes.

Regressions: `registry/endpoints_key_test.go`, `registry/cacher_endpoint_split_test.go`,
`selector/filter_test.go`.

### Subscribers: only `func(*TSubscriberContext)` can ever run

`handler.InvokeSubscriber` walks `handler.funcs` and calls `handle.SubFunc`, whose type is
`func(*TSubscriberContext)`. Anything that does not end up in `funcs` with that shape is
registered, advertised in the registry, delivered to this process — and never executed.

`parseSubscriber`'s two branches used to disagree about that. The plain
`func(*TSubscriberContext)` case filled `funcs`; the reflection case (a struct's methods, or a
func arriving as a `reflect.Value`) built the handler and returned without ever populating
`funcs`. So **struct subscribers silently never fired**, with `Subscribe` returning nil. Two
further faults in the same branch confirm nobody had ever taken it:

- The methods were collected with `Type().Method(i).Func`, which is **unbound** — its first
  parameter is the receiver, so the signature is `func(T, *TSubscriberContext)`, matching
  nothing and with no receiver to pass. `Value.Method(i)` is the bound form.
- `if v, ok := sub.(IString)` shadowed the type-switch's own `v`, so the `else` branch handed
  `utils.Obj2Name` the **failed** assertion's nil interface — `reflect: call of
  reflect.Value.Type on zero Value`. Any struct subscriber not implementing `IString` panicked
  on registration.

Struct methods now work, and must be **value receivers** with exactly that signature — the
same rule as controllers and middleware, for the same reason (the method set of a value
excludes pointer-receiver methods). Methods with any other signature are skipped, since a
struct normally has unrelated methods too.

`validateSubscriber` was `return nil`. It now rejects any handler with an empty `funcs`, so a
shape that cannot execute fails at registration instead of surfacing later as "that event does
nothing". Regression: `router/subscriber_test.go`.

### Broker: one instance per process

`broker.Use(name, opts...)` caches instances. The key is the **resolved** backend name
(an empty or unregistered name falls back to `http`), plus `name-addr` when the caller
supplied a real address. A caller with no usable address gets the bare-name slot — which
is how `broker.Default()` and a `server` with no `BrokerHost` come to hold the *same*
object. That sharing is load-bearing, not an optimization:

- **Only the server's broker is given a real registry** (`server/config.go`'s
  `WithRegistry` → `cfg.Broker.Init(broker.WithRegistry(r))`). A second instance keeps the
  `NopRegistry` it was born with, whose `GetService` returns `(nil, nil)` — so `Publish`
  iterates an empty node list, sends nothing, and **returns nil**. Both sides' logs stay
  clean; the only symptom is "the UI didn't update", which looks exactly like "the business
  event never fired". `server` therefore also calls `broker.SetDefault(bk)`, since library
  code reaches the bus through `broker.Default()`.
- **Only the server's broker is `Start()`ed**, so only it has a listening port. Subscribing
  on an unstarted broker would register a node whose port is `0`; `httpBroker.Subscribe`
  now `Start()`s first (it is idempotent) rather than publishing an unreachable address.
- A port of `0` is not an identity (`usableAddr`): `DefaultAddress` is `127.0.0.1:0` and
  `WithBroker` copies `bk.Address()` back into `cfg.BrokerHost`, so once that value lands in
  config.json the next boot would look like an "addressed" call and split the process again.

Sharing has a cost to know: the first `Stop()` closes the broker for every holder, and
`Address()` is read during a later server's assembly — hence the exit goroutine's address
restore must happen **under the lock and before the `ch` handshake**, or a `Close`→`Start`
sequence overwrites the newly bound address with `127.0.0.1:0`.

**Broadcast is a cross-process string contract.** A subscription without a queue registers
`registry.Service.Version = config.BroadcastVersion` (default `broadcast`, i.e.
`broker.DefaultBroadcastVersion`), and the publisher decides fan-out-to-all vs. pick-one by
comparing that string, so both sides must read one source — `httpBroker.broadcastVersion()`.
They used not to: the publisher compared a package var `"volts.http.broadcast"`, so **every**
broadcast subscription fell into the queue-group branch and went to one random node. It is
invisible in-process, because all subscriptions of one broker share a single `node.Id` and
one POST fans out to every local handler — which is why `TestConcurrentSubBroker` stayed
green. Regressions: `broker/http/broadcast_test.go`, `broker/use_instance_test.go`.

**The delivery response's status code is the only feedback channel a publisher has**, and
`broker/http/handler.go` gives it three meanings — get one wrong and the failure is silent
in a different way each time:

- **4xx = permanently refused** — unparseable message, no `v-topic`, or no subscriber with
  that `id` (the registry entry is stale: unsubscribed, or the process restarted with a new
  uuid). Retrying changes nothing, so the publisher drops it rather than replaying a batch
  of doomed messages ahead of every real one.
- **5xx = retryable**, kept in the backlog.
- **2xx = delivered — including when the handler returns an error.** The message *did*
  arrive; what failed is the application. Answering 5xx would push it back into the backlog,
  turning "a handler is broken" into "every subsequent message is consumed twice" — and
  delivery here is at-most-once, so callers owe no idempotency. Handler errors go to
  `Config().ErrorHandler` (which the http backend previously ignored entirely) or the log,
  per subscriber; a panicking handler is recovered so it cannot take its siblings' delivery
  down with it. All of this used to answer 200 unconditionally: a message could be sent,
  received by nobody, and leave both sides' logs clean.

Delivery is **at-most-once**: `event.Ack()` is a no-op in both built-in backends and
`SubscribeConfig.AutoAck` / `DisableAutoAck()` are not implemented by either — there is no
redelivery window to acknowledge. The retry backlog is 64 messages per topic in the
*publisher's* memory (`maxInboxSize`), drained 8 at a time and **only** by the next
`Publish` on that same topic — a topic nobody publishes to again never retries — and
overflow now drops the **oldest**, where it used to keep the oldest 64 and discard each
newly arriving message. Use this broker for cache-invalidation-shaped signals — the message
carries a key and the receiver re-reads the source of truth — not for messages that must
not be lost.

**Unsubscribing deregisters the node only when the topic has no subscribers left.** All
subscriptions on one topic share a single `node.Id` (`topic + "-" + broker uuid`) and hence
one registry record, so an unconditional `Deregister` silently removes the whole *process*
from that topic's delivery list. `Topic()` / `Config()` / `Unsubscribe()` were all stubs
before — `server.Stop()`'s unsubscribe loop never did anything, and `sub.Config().Queue` was
a nil dereference on the http backend while working fine on memory.

`Publish` treats `registry.ErrNotFound` as "no subscribers", not as an error: `NopRegistry`
returns `(nil, nil)` and memory/etcd return `ErrNotFound` for the same situation, so passing
it through made the same business code start reporting failures when the registry changed.

Regressions: `broker/http/delivery_test.go`.

### broker/redis: swapping the backend swaps the contract

`broker/redis/` keeps messages **in redis, not in the publishing process's memory**. Opt in
with a blank import, exactly like `registry/etcd` — it is deliberately absent from
`server`'s import list, so go-redis stays out of binaries that never ask for it:

```go
import _ "github.com/volts-dev/volts/broker/redis"
// server.WithBrokerType("redis"), server.WithBrokerHost("redis://:${REDIS_PASSWORD}@host:6379/1")
```

Three properties invert what the sections above document, so code written against the
built-in backends is not automatically correct here:

- **Delivery is at-least-once and `IEvent.Ack()` is real** (an `XACK`). `DisableAutoAck()`
  actually works. Callers now owe idempotency — the exact obligation the http backend
  explicitly does *not* impose.
- **Publisher and subscriber need not overlap in time.** The http backend's backlog is 64
  messages in the publisher's RAM, gone when it exits; here the backlog is the stream itself.
- **The registry is unused** (`WithRegistry` is a no-op). The whole "two mutually unknown
  broker instances, one holding a `NopRegistry`, silently dropping every message" failure
  class does not exist on this backend.

Semantics map onto consumer groups: one topic is one stream; subscriptions sharing a `Queue`
share one group (messages split across them); a subscription without a queue gets **its own**
group (every group sees everything).

Rules that are easy to get wrong:

- **Broadcast needs one group per *subscription*, not per process.** A group splits messages
  internally, so two local broadcast subscriptions sharing one group would each see half —
  where http's single POST fans out to every local handler. Group names carry a slot index
  (lowest free), so unsubscribe-then-resubscribe reuses the name, which is what lets a
  configured `WithBroadcastID` resume its read position across a restart.
- **Liveness is a heartbeat key, never the consumer's `idle`.** Measured on redis 7.0.4: an
  `XREADGROUP` that returns nothing does **not** refresh the consumer's seen-time (151ms →
  209ms across an empty read) and does not even register the consumer. So "no consumers /
  idle for a day" is precisely what a healthy low-traffic subscriber looks like, and an
  idle-based sweep destroys live groups. The damage is near-invisible: the read loop's
  `NOGROUP` branch rebuilds the group at `$`, so the symptom is "every so often a run of
  messages goes missing". Each broadcast subscriber therefore renews `<stream>:hb:<group>`
  (throttled to `GroupTTL/4`), and the sweep only destroys broadcast groups whose key has
  expired. Queue groups are never swept — they are shared, and this process cannot know
  whether another one still uses them.
- **`AutoAck` defaults to true, so build the config with `broker.NewSubscribeOptions`.** A
  hand-rolled `for range opts` over a zero `SubscribeConfig` — which is what `broker/memory`
  does, harmlessly, since it never reads the field — yields `AutoAck=false`: nothing is ever
  acked and every message is redelivered after `ClaimMinIdle`. It surfaces as "messages
  occasionally duplicate", never as a config error.
- **A handler error must NOT ack** — the opposite of the http backend's rule, because here a
  real redelivery window exists. `MaxDeliveries` (default 5) then bounds it: a message that
  can never be handled would otherwise hold the head of the PEL forever, with every genuine
  retry queued behind it.
- **`MaxLen` (default 10000, `XADD MAXLEN ~`) is a bounded buffer, not a durable queue.** A
  subscriber down long enough for its backlog to be trimmed has lost those messages. Same
  shape of guarantee as the http backend's inbox, just far larger and outside the publisher.

Tests spin up their own `redis-server` on a **unix socket** (no fixed ports; the path is kept
short because macOS `sun_path` is 104 bytes) and skip when the binary is absent.
`TestUnsubscribe_StopsDelivery` deliberately uses a *queue* subscription: destroying a
broadcast group unblocks `XREADGROUP` with `NOGROUP` immediately and the rebuilt group starts
at `$`, which masks a read loop that never stopped.

Regressions: `broker/redis/redis_test.go`.

### Registry-Driven Route Updates

`router/router.go:store()` is called on Registry watch events and periodic refresh (every 60s) to update proxy routes pointing to discovered services. Discovered nodes that resolve to **this process itself** are dropped by `filteSelf` (matched by `node.Id` **or** `addr.LocalFormat`-normalized address, so a same-address / different-id node from a `Debug()` port collision is still caught); otherwise a self-referential proxy route would replace the local handler and loop back into `127.0.0.1:<own port>` until ephemeral ports exhaust (`connect: can't assign requested address`). `router/reverse_proxy_loopguard.go` is the registry-independent runtime backstop: a hop-depth header caps any forwarding cycle, and a self-address short-circuit (via `http.LocalAddrContextKey`) refuses to dial the process's own listen port. That short-circuit matches loopback **and** any local NIC address — the registry stores NIC addresses (`192.168.x.x:35999`), and when `LocalFormat`'s fold-to-loopback misses (10s IP cache vs. a network switch, or a zombie node advertising a stale IP) a loopback-only check is bypassed entirely.

**Only HTTP endpoints get proxy routes.** `store()` skips `CONNECT` (RPC) endpoints: there is no RPC reverse-proxy implementation — `RpcReverseProxy` was an empty function body, so the route silently swallowed the request and the caller waited out `RequestTimeout` (20s) for a `context deadline exceeded` with nothing in the log pointing back here. Worse, `TTree.addNode` replaces same-path routes, so that empty handler would evict the local RPC handler and turn working local calls into black holes. Cross-process RPC dials the target node directly via `client` and never transits the gateway, so nothing needs proxying. A request for a service this process doesn't host now gets `StatusNotFound` immediately.

**Discovered proxy routes are now reclaimed.** `store()` used to only ever add, and
`TRouter.Deregister` has no production caller at all, so a service whose last node
deregistered kept its proxy routes forever — the path answered **503** ("retry later", which
clients and gateways duly do) where the honest answer was 404. `router/proxy_route_reclaim.go`
books what `store()` actually built, per service, together with the proxy handler id, and
removes the difference on the next round, on a watch `delete`, and on a `refresh()`
reconciliation against `ListServices`.

Removal is safe because `TTree.DelRoute` strips **by handler id** (`route.stripHandler`) and
only drops the node once nothing is left on it; the id is a crc32 of `service:path:…`, so it
cannot collide with a local handler or another service. Two further gates sit on top, and
only one of them is load-bearing:

- **A path still claimed by another service is never removed** — this one matters. Two
  services advertising one path share a single tree route (last writer wins), so removing the
  later one would take the path away from the earlier one until the next refresh, up to 60s.
  The regression must drop *the service whose handler is currently on the tree*; dropping the
  other proves nothing, because the id-recheck has already given up by then.
- **The id is re-checked against the tree before deleting** — defensive only. Missing the
  strip is harmless; the check just avoids a pointless write-locked traversal and keeps
  `TTree.Count` (a display counter) honest.

An erroneous reclamation is self-healing: the next `store()` rebuilds the route. Not
reclaiming is permanent. That is the same bias as `isLocalService` — prefer one extra pass
over a bad route nothing will ever correct. Regression:
`router/proxy_route_reclaim_test.go`.

`registry.Service.Equal` decides (via `TRouter.isLocalService`) whether `refresh()` skips a service entirely, so a wrong `true` means **permanently no route**. It compares name, version, node count, and node ids — historically the whole comparison sat inside `if len(a.Nodes) == len(b.Nodes)` and fell through to `return true` when counts differed, which any zombie node (SIGKILL leaves the etcd lease un-revoked, so old+new nodes coexist and the count doubles) turned into "remote service misread as local". Keep the bias toward `false`: a false negative only costs one extra `store()` pass, where `filteSelf` filters precisely.

**A local route always wins over a discovered proxy route.** `filteSelf` is a *best-effort* filter — it only works when the process can recognize itself, and in the real vectors topology it routinely cannot: one `TRouter` is shared by the HTTP and the RPC server, each `server.WithRegistry` wraps the registry in its **own** `registryCacher`, and the router keeps only the last one — so `LocalServices()` never sees the sibling server's node, and every self-hosted service comes back through discovery looking remote (kylin logs one shield warning per service at startup; that is this, not a new fault). Three defenses now sit behind it, in `store()`:

- **`hasLocalRoute` (the shield).** An endpoint already served by a local handler never gets a proxy route. Without it, `TTree.addNode`'s last-writer-wins evicted the local handler *permanently* — `refresh()` afterwards classifies the service as local and skips it, so nothing ever repairs the tree. 2026-08-01, kylin: `/gamification/static/security/*.xml` answered 508 forever, and since module install/upgrade fetches manifest files over `GET /{service}/static/{file}` (`core/download`), the whole upgrade died with `prefetch module data: ... Failed to download data file`. Which modules got hit changed on every restart — it is a race, so "it works for account, therefore routing is fine" proves nothing.
- **`filteHttpNodes`.** HTTP proxy routes are only built from nodes that can actually speak HTTP (`metadata["protocol"]` http/https; unlabelled nodes are kept). The http and rpc registrations of one process carry the **same full endpoint table**, so a record left holding only the RPC node still lists the HTTP paths — and `getService` then re-selects an http node at request time, which is this process's own port. Route built from one node, dialed to another: instant self-loop.
- **`registryCacher.Register` records local identity *before* publishing.** The registry write is what wakes this process's own watcher, which drives `store()`; updating `localServices` afterwards leaves a window where the process cannot recognize its own brand-new registration. `LocalServices()` also keeps only *our* nodes out of the registry's merged read-back — pulling in a peer's node would make `filteSelf` drop a genuinely remote node, i.e. permanently no route, the same damage mirrored.

Regressions: `router/store_local_route_shield_test.go`, `registry/cacher_register_order_test.go`.

(The former `handler.Services`/`SetServices` field — once documented here as a data race — is gone: it had no readers and `SetServices` was never called, so the race could not occur. Removed rather than locked.)

### Config: the file is a read-only input

**The config file is never written back automatically.** `Register` seeds a model's current
values as **defaults** (viper's defaults layer), `LoadFromFile` does not flush, and
`SaveToFile` persists **only the diff** — values someone actually set at runtime, plus keys
that were already in the file. `Dump()` is the one exit that emits defaults, for generating
an editable template.

Three rules that are easy to get wrong:

- **Defaults go to the defaults layer, never to override.** `SetValue` writes viper's
  *override* layer, which outranks the config file; `SetDefault` writes *defaults*, which the
  file outranks. Seeding a model's defaults through `SetValue` means the file can never win
  — the user edits config.json and nothing happens, with no diagnostic. There are **two**
  seeding paths (`Register`, and `LoadToModel`'s "section missing → `model.Save()`" fallback);
  fixing only one leaves the symptom identical. Both go through `saveDefaults`.
  Regression: `TestFileOverridesCodeDefaults`.
- **Writing defaults back is what made `if cfg.X == 0 { fallback }` dead code.** Once a
  default lands in the file it *is* user config, and changing the constant stops mattering.
  kylin's config.json held 453 keys, 400+ of them defaults, with `registry.timeout` copied 15
  times — which is why raising the etcd timeout from 100ms to 5s had no effect anywhere.
- **Duration units must match on both sides.** `SaveFromModel` persists
  `d.Milliseconds()`, `withDurationHook` decodes ints as ms — `GetDuration` used to do
  `time.Duration(GetInt64(key))`, i.e. nanoseconds, so the two read paths disagreed by 10⁶.
  Bare numbers are milliseconds; unit-suffixed strings (`"5s"`) are preferred in files.

**Secrets can be externalized.** A config value of the form `${VAR}` is resolved from the
environment at load time; `${VAR:-default}` declares it optional. An unset `${VAR}` with no
default is an **error**, never a silent empty string — an empty signing key lets the service
come up with signature checking degraded to "anyone can forge", which is worse than failing
to start. Expansion happens on the **model only**: the config tree keeps the raw `${VAR}`
literal, so no later `SaveToFile` can bake a resolved secret into the file. Values without
`${}` are untouched, so existing files need no changes. Regression:
`TestExpandEnvRefs_NotWrittenBackToFile`.

**Diagnostics.** Load errors accumulate (`config.Errors()`) instead of `log.Fatal` — a library
must not `os.Exit` on the caller's behalf, and vectors registers configs at runtime.
`service.Start()` is the one place that knows every component has registered, so it reports
errors, `Lint()` (unknown keys with spelling suggestions) and `Validate()` there, and prints
`Inspect()` when `VOLTS_CONFIG_REPORT` is set.

**`Lint` is deliberately conservative; `Report` is not.** Config models register **lazily** —
a section is only known once code actually builds that component. Lint therefore skips any
section nothing claims, and only reports keys whose *parent path* is a structure some model
declared (`server.http` is registered, but `server.http.client` is a sub-component's section
that the server model never declares). Loosening either rule reintroduced 300+ false
positives against kylin's config. A check that cries wolf daily gets ignored along with the
real typos. Whole-section orphans are `Report`'s job instead.

**Never name a `*Config` method after an interface the models might satisfy.** Every config
model embeds `config.Config`, so methods get promoted onto all of them — naming the
tree-wide validator `Validate()` made every model "implement" `IValidator` with an inherited
body that walks all models, i.e. infinite recursion and a stack overflow. Hence `ValidateAll`
/ `ReportAll` on the type, with plain `config.Validate()` / `config.Inspect()` as package
functions (those are not promoted). Regression:
`TestValidate_SkipsModelsWithoutValidator`.

Validation errors carry their own location: models return `config.Invalid("read_timeout", …)`
with a **relative** field name and the framework prefixes the full path plus file line
(`config.json:47: server.http.transport.read_timeout: …`). A component cannot know where it
is mounted — transport may sit under `server.http` or `client.rpc` — so it must not build
that path itself. Validation runs bottom-up, so a parent may assume its children are valid.

### Known Issues (tracked in memory)

The original 20-item concurrency/correctness audit (see project memory) has been fully
dispositioned as of 2026-07-17 — every CRITICAL/HIGH item and each named MEDIUM item is
either fixed or verified already-fixed in current code (WebSocket proxy leak, context
pool race, SIGKILL, `registered` field, `handler.Services` lock, pool-key growth via
`TTree.onRouteReplaced`, router lifecycle via `TRouter.Close()`, Stop error aggregation,
broker/http Close deadlock + transport double-close).

A second, wider audit (security / stability / efficiency / stack) ran 2026-07-20..21 —
21 items, all actionable ones fixed on branch `fix/security-audit-p0`. Acceptance:
`go test -race ./...` clean, `go vet ./...` clean, 3 fuzz targets ~5M execs with no crash.
See project memory for the full list. Remaining open notes:

- **config hot-reload is still racy on the read side** (`config.WithWatcher()`, which
  vectors enables in production). Reload now decodes into a *copy*, validates it, and
  writes back **only the fields that actually changed** — so most reloads write nothing at
  all, an invalid file no longer corrupts a live instance, and the config layer's own
  write paths (Register / LoadFromFile / Reload) are serialized by `core.applyMu`.
  What remains unfixed: when a field genuinely changes, that write still races against
  request goroutines reading the same struct field directly (`router.Config.Recover` per
  request, `transport.Config.ReadTimeout` per connection, `selector.Config.BreakerFailures`
  per Select). Closing it needs copy-on-write + accessor reads across every read site —
  a read-side change, not a config-layer one. Do not "just add a mutex" there: the readers
  are arbitrary business code touching struct fields directly.
  Reload must keep skipping `field:"-"` when diffing — those are live instance pointers
  (Logger / Registry / Transport / Router); `UnmarshalKey` never populates them, so the
  copy holds nil, and diffing them would "detect a change" and null out the running
  registry and transport. Regression: `TestReload_OnlyWritesChangedFields`.
  A model can opt out or take over via `config.IReloadable`: returning
  `config.ErrReloadUnsupported` makes the framework touch **nothing** (the only way to get
  the race to zero — right for anything that needs a restart to take effect anyway, like
  listen addresses or DB connection strings); returning nil means the model applied the
  change itself under its own lock. The change list is computed *before* `OnReload`, since
  afterwards the model already equals the new copy and nothing would diff.
- `IClient` interface completeness is a design gap, not a bug. It lacks `NewRequest`
  because HttpClient (`method, url, data`) and RpcClient (`service, method, request`)
  differ semantically — code needing it must hold the concrete type.
- broker/http `Publish` must keep `maddr.LocalFormat` on node addresses (macOS network
  extensions intercept traffic to physical interfaces — same rule as client/reverse_proxy)
- go.mod carries a local `replace` for volts-dev/cacher until its Push race fix is
  published. **Until then this repo cannot be `go get`-ed by anyone else.**

### Testing Notes

- Tests that wait on an external event **must** carry a timeout. Three separate packages
  (logger, mdns×2, internal/test/client) each used to hang for the full test timeout
  (~300s), which drowned every real result. A hang is worse than a failure: it reports
  nothing.
- Do not bind fixed ports in tests. `server.Debug()` pins 35999/45999, which collides with
  a locally running kylin.
- **TCP reachability probes do not work on this machine.** macOS network extensions accept
  every TCP connect — `net.DialTimeout` to `192.0.2.1` (RFC 5737, unroutable) succeeds in
  0.01s. Tests hitting public endpoints must classify the *request* error instead
  (see `client/http_client_test.go:skipIfNetworkError`).
- **Measuring a proxy on this machine measures Clash, not the proxy.** A local Clash/mihomo
  in TUN mode intercepts every outbound TCP — *including a dial to a literal IP* — and
  re-routes it by its own rules, so "these nodes fail" may mean "mihomo tunnelled our
  handshake through a second hop". The tell is a local address in `198.18.0.0/15`; the proof
  is mihomo's own connection table
  (`curl --unix-socket /tmp/verge/verge-mihomo.sock http://localhost/connections`), which
  names the chain it picked. `transport/ss_live_test.go` (`VOLTS_SS_LIVE_NODES=<file>`) is
  the real judgment: it prints the **exit IP** per node, so a working proxy is visible as a
  different IP rather than as a 200 that direct traffic would also produce.
- **`httptest.ResponseRecorder` does not enforce net/http's body rules, and the real
  server does** — so a 304 or 204 carrying a body is visible in a unit test and invisible
  through `httptest.NewServer` (measured: 18 bytes written, 0 bytes on the wire; net/http
  answers `ErrBodyNotAllowed`). The two instruments disagree, which is worse than either
  being wrong: trust the recorder and you chase a defect that does not exist in production;
  trust the live server and you miss the day a different writer (the compression wrapper, a
  hijacked connection) actually lets it out. `THttpResponse.Write` therefore applies the
  rule itself. Regression: `router/http_bodyless_test.go` (recorder) — deliberately not a
  live test, since a live test passes either way.
- `transport/message_fuzz_test.go` holds the fuzz targets; deep runs with
  `gotip test -run=XXX -fuzz=FuzzMessageDecode -fuzztime=10m ./transport/`.
- **Measure before calling something an optimization.** `router/handler_bench_test.go`
  exists because a "hoist the type assertion out of the loop" change was committed as a
  perf win and turned out to be 15% *slower*. In short chains the surrounding ServeHTTP
  work (route match, context pool, ~19 allocs, ~2µs) drowns out anything the dispatch loop
  does — use the 50-middleware case, `-count=5`, and compare medians.

### Optional Submodule: `core/ja3`

`core/ja3/` is a **separate Go module** (`github.com/volts-dev/volts/core/ja3`) holding the
TLS fingerprint-spoofing implementation. The main module deliberately does not depend on it,
and therefore does not depend on `github.com/refraction-networking/utls` — a library whose
purpose is mimicking other clients' TLS fingerprints, which most people writing business
services do not need but would otherwise compile into every binary and have flagged in
compliance scans.

Enable it with a blank import:

```go
import _ "github.com/volts-dev/volts/core/ja3"
```

**Prefer a preset name over a JA3 string.** `Dial(conn, host, cfg, "chrome_133")` uses
utls's byte-exact ClientHello; a JA3 string is only ever an approximation, because JA3
records extension **ids** and not their contents — and GREASE, Chrome's per-connection
extension shuffling, the post-quantum key share and ALPS all live in the contents.
Measured (`tls.peet.ws`): the preset yields JA4 `t13d1516h2_8daaf6152771_…` (Chrome's real
cipher hash); a hand-copied JA3 string yields `t13d3112h2_e8f1e7e78f70_…` — 31 ciphers where
Chrome sends 15, i.e. a client that is *nobody*, and therefore rarer than Go's own default.
The dispatch is by shape: a JA3 string always has exactly four commas; anything else is
looked up as a preset name, and an unknown name is an error that lists the valid ones.

**Rebuilding a ClientHello may only restore what the string contains — never invent.**
A GREASE key_share added to a string whose curve list has no GREASE violates RFC 8446
§4.2.8 (no key_share for a group absent from supported_groups). Go's own TLS server does
not check this, so httptest stays green; Akamai does, and answers `illegal parameter` —
an error that names neither extension. Guards must therefore be **structural assertions**
over the spec (`TestKeyShareIsSubsetOfSupportedGroups`), not a successful handshake.
`boundary_live_test.go` (`VOLTS_JA3_LIVE=1`) handshakes every preset against Akamai,
Cloudflare and Google — that is the only thing that answers "does this fingerprint work".

`transport` keeps two things: the hook (`transport/ja3_hook.go` — the `Ja3` config struct,
the dialer registry, `Ja3Dialer()` for callers building their own `DialTLSContext`, and
`INegotiatedProtocol` so callers can read ALPN without asserting a concrete TLS type), and
the **metadata registry** (`transport/fingerprint.go` — each fingerprint's name, browser
family and self-consistent User-Agents). Metadata needs no utls, so keeping it in the main
module lets upper layers enumerate identities, pair UA with fingerprint, and branch on
family — without writing utls into their own go.mod. Setting a fingerprint **without**
importing the submodule returns `transport.ErrJa3NotRegistered` rather than silently
falling back to ordinary TLS — a silent fallback would leave the caller believing they
were disguised.

The JA3 integration tests live in the submodule (`core/ja3/client_integration_test.go`);
they cannot stay in the main module, since importing the submodule from there would both
create a module cycle and drag utls back into the main dependency tree.

### Outbound: talking to strangers

`core/outbound/` is the HTTP client for scraping **third-party** sites. `client/` calls
*your own* services (discovery, balancing, breakers, RPC codecs); this one calls people who
do not know you and have no obligation to cooperate, so the question shifts from "how do I
find it" to "why would it answer me". Both share transport's dialing and fingerprint
registry; nothing above that is common, so they stay separate.

```go
cli := outbound.Client()   // a plain *http.Client from here on
```

An identity is **fingerprint + UA + client hints + exit IP**, pinned together at pool build
time, each with **its own connection pool** — sharing one `http.Transport` would let a
connection that handshook as mac Chrome carry a request whose UA says win Chrome, and two
identities on one TLS connection is more recognisable than no disguise at all. UA comes
from the fingerprint and `sec-ch-ua` is **derived from the UA**, so there is no opportunity
to mistype either; family decides whether client hints are sent at all (Chromium only —
Firefox sends `Sec-Fetch-*` but no hints, Safari sends neither).

**Being refused is not a transport error.** A 403 arrives over a connection that worked:
TCP connected, TLS handshook, the peer answered. To `http.RoundTripper` that is a *success*,
so without special handling a blocked identity keeps hammering the same endpoint with the
same fingerprint forever — which is precisely the case the disguise exists for. Statuses in
`RotateOnStatus` (default 403 + 429) therefore mean "this identity is blocked": cool it
down, **drop its idle connections** (WAF verdicts often bind to the connection/session, so
keeping it warm makes the cooldown meaningless), and retry on the next identity. When every
attempt is refused the **last response is handed back verbatim** — the caller needs to see
the 403 itself, not a rewritten error. 429's `Retry-After` outranks our own backoff (both
delta-seconds and HTTP-date), still capped at `cooldownMax`.

503 is deliberately **not** in the default list: it is both Cloudflare's challenge page and
"the site is actually down", and the response cannot tell them apart — treating it as a
block multiplies traffic to an already-struggling peer by `maxAttempts`.

**Proxies can be swapped at runtime, and they may be `ss://` / `ssr://`.**
`outbound.SetProxies(urls)` replaces the list and rebuilds the pool; `nil` drops the
override (back to config.json), an empty slice means "nothing configured" (= direct), and a
**failed build keeps the old pool** — refusing the change is better than swapping in an
empty pool that silently kills every exit. A caller must push the proxies the operator
*enabled*, never the ones that last *tested green*: filtering on health makes one network
blip empty the list, and "configured a proxy" quietly becomes "direct", which is exactly
what `allow_direct=false` exists to prevent.

`transport.NewProxyDialer` speaks `http`/`https`/`socks5` **plus `ss` and `ssr`**
(`transport/ss_proxy.go`), because the proxies people actually buy in the Chinese market
come as airport subscriptions whose nodes are ss/ssr — supporting only http/socks5 makes a
subscription import land 149 nodes of which **0** are usable. The line is drawn at ss
(AEAD + stream ciphers) and ssr with `protocol=origin`, `obfs=plain|http_simple` — that tier
is really just classic shadowsocks plus an HTTP-shaped header, whereas `auth_chain_*` is a
second stateful protocol machine and hysteria2/tuic need a whole QUIC stack. Everything else
**errors and names the protocol**; guessing "let us try it as origin" builds a connection
that then silently drops every packet server-side. Layer order is load-bearing —
`payload → [SS cipher] → [SSR obfs] → TCP`, obfs wrapping the *ciphertext* — and CFB is
**not symmetric** (encrypter one way, decrypter the other; sharing one still "connects",
then every byte is wrong). `TestSsRoundTrip` runs every cipher against an in-process fake
server for exactly these "connects fine, data is garbage" failures.

`RedactProxy` blanks ss/ssr URLs **entirely** (keeping only the `#name`): their credentials
are not in front of the `@`, so the old `@`-based redaction left passwords in the log.

**Concurrent requests on one line are a signal in themselves.** A line is an identity —
fingerprint + UA + exit IP. Two requests in flight on one line means either a second TCP
connection (h1) or a second stream (h2), i.e. one browser on one IP issuing several
unrelated requests in the same millisecond. So each identity carries a gate of capacity 1
(`MaxPerIdentity`): a request that cannot get a slot **waits** rather than going out on an
identity that is already busy. Parallelism is not cancelled, it is redirected onto other
lines — which is also the right way to add throughput: add an exit, don't widen the gate.
The slot is held until the **response body is closed**, not until headers arrive (the
connection is still transferring when headers come back, so releasing early lets the next
request overlap after all); `maxSlotHold` is the leak valve for a caller that forgets
`Body.Close()`, because turning a leaked connection into a permanently wedged line would
be a worse failure than the one being prevented. `outbound.Concurrency()` reports
lines × gate, so a caller's own worker pool can be sized to it — a pool wider than that
just queues, and that queueing is charged to the caller's own ctx, which turns "we
oversubscribed" into what looks like "the peer timed out".

Seven rules that are easy to get wrong:

- **One exit means one identity — but only in steady state.** Rotating six browser
  identities out of a single IP, each polling the same endpoints on the same schedule, is
  *more* machine-like than one stable identity — real people behind one exit do not behave
  identically. So the *active* identity count follows proxy count. The leftover fingerprints
  are not discarded: they become **reserves**, picked only when every active identity is
  cooling. Once you have been refused, "looking natural" has already failed, and the choice
  is between an identity that is certainly blocked and one that might not be. Naming
  `profiles` explicitly means "these exact ones" — all active, no reserves.
- **Reserves cover *blocked*, never merely *busy*.** Falling through to a reserve because
  the active is mid-request would put two identities on one exit simultaneously — which is
  the "same IP, several browsers" pattern this package argues against. A busy active is
  worth waiting for; a blocked one is not. Regression:
  `TestBusyActivesDoNotPromoteReserves`.
- **Rotation cannot change the IP.** With no proxy configured all identities share one exit,
  so an IP-based limiter is unaffected by any amount of fingerprint rotation. The first
  block logs this once (`warnIfDirect`) rather than letting someone watch a feature that is
  "already rotating" and guess why nothing improves.
- **Replay needs `GetBody`, not just an idempotent method.** A refusal means the peer never
  processed the request, so replaying is *semantically* safe — but `Body` is a one-shot
  reader, and retrying without rewinding silently sends an **empty** request whose symptom
  ("the peer says the parameters are wrong") points nowhere near the cause. `http.NewRequest`
  fills `GetBody` for bytes/strings bodies; anything else is not retried.
- **A configured proxy is never silently bypassed.** `allow_direct` defaults to false:
  when every proxy is unusable the request fails with `ErrNoUsableProxy` instead of going
  out on the host's own IP. People configure proxies precisely so the far end does not see
  that IP; a silent fallback quietly does the opposite, with no trace.
- **ALPN must be probed explicitly.** `http.Transport` reads ALPN by type-asserting
  `*tls.Conn`; a utls connection is not one, so the transport speaks HTTP/1.1 no matter what
  the peer selected — and an h2 peer receiving HTTP/1.1 bytes produces an error
  indistinguishable from "the site changed its response format".
- **HTTP/2 fingerprints cannot be matched, and half-matching is worse than not trying.**
  Go's SETTINGS are `2:0;4:4194304;5:1048576;6:10485760|1073741824|0|a,m,p,s`; Chrome 133 is
  `1:65536;2:0;4:6291456;6:262144|15663105|0|m,a,s,p`. Setting 5 is one Chrome never sends
  and Go always does, the order differs, and the pseudo-header order is hardcoded in
  `encodeHeaders`. Tuning the reachable knobs turns the **most common fingerprint in the
  world** into a unique one — from hiding in a crowd to being the only sender. So none of
  them are tuned.

Disguise failure is **completely silent**: drop the `core/ja3` blank import and everything
still compiles, tests pass and requests return 200 — they just go out as Go. Any service
relying on it needs a wiring guard asserting `outbound.Status()` reports no
`no-fingerprint` identity.

