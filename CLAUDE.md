# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

**细节分册在 [docs/claude/](docs/claude/)。** 这份文件只留「每次都要知道」的部分：命令、
架构、约定，以及一份索引。索引里每一行就是规则本身，链接后面才是它的来龙去脉。
**动到哪一块，先把那一册读完再改。**

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

**Options pattern** — all configuration uses `func(*Config)` closures. Every package exposes `New(opts ...Option)` and a `Default()` singleton. Options only write struct fields; they never touch the config tree — binding happens once, at the end of `newConfig` (see "Config: the tree is the struct graph").

**`String()` method** — every object must implement `String()` returning its name (see doc.go). Config structs are the exception: they are plain data and have no `String()`; their path is given by whoever binds them.

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
| `logger/` | Self-contained leveled logger (writer manager + console writer, plus process-wide structured `Sink`s). **Not** built on zap — `go.uber.org/zap` is pulled in only by `registry/etcd` for clientv3's `LogConfig` |
| `internal/` | Shared utilities (pool, metadata, TLS helpers, mDNS, ACME, backoff) |
| `core/outbound/` | Outbound HTTP for scraping **third-party** sites — browser identity rotation (TLS fingerprint + UA + client hints + proxy exit). See "Outbound: talking to strangers" |
| `config/` | Config tree: `Bind(path, ptr)` binds a plain struct, nested structs are subsections; load/validate/lint/report/hot-reload walk the struct graph. viper is the file codec only — layering, provenance and diff-persistence live in this package (see "Config: the tree is the struct graph") |

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

## Invariants and hard-won detail — 索引

细节分册在 [docs/claude/](docs/claude/)。**下面每一行就是规则本身**，链接后面才是它的
来龙去脉：判据、真栈量到的数、坏掉时看起来像什么、回归测试在哪。动到哪一块就先把那一册
读完再改 —— 这些条目几乎每一条都是"改对了才发现原来一直是坏的"。

### HTTP 出口 — [docs/claude/router-http.md](docs/claude/router-http.md)

- **框架永远不服务工作目录。** `UseRootStatics` / `StaticDir` / `StaticExt` 已整块删除；把它"修对"等于给每个进程装一个工作目录文件服务器。要加根级静态服务，先把 `TestRouter_DoesNotServeTheWorkingDirectory` 弄红。
- 静态文件：**modtime 才换得来 304**（embed 借可执行文件的 mtime），且必须与字节一起缓存；超过 4MiB 的直接流式，不进内存。
- `router.WithCompress()` **默认关，这是安全决定不是谨慎**（BREACH）。它不是中间件也不能是；判断延到 `minSize` 之后，`Flush` 强制决定；压了就要改 `Content-Length` / `ETag` / `Vary` 三个头。
- **池化的 context 每个字段都要在 `reset` 里有归宿**，`self.context` 也是（2026-09-24 修）。漏了它，鉴权中间件**认之前**就 401 的那一支会读到上一位登录用户的会话——vectors 的 `AuthSession`/调用方身份/租户作用域全走这条路。没有任何报告，只在同一路由连着两个请求时出现。
- `WriteHeader` 只是**记下**状态码，真正发出在第一次 `Write` / `Flush` / `Commit()`；204 / 304 / 1xx 不许带正文。
- WebSocket：升级成功即**脱离 context pool**；`CheckOrigin: nil` 是同源不是放行（CSWSH）；读上限是承重的（每帧一次、合并分片再一次）；一个读协程、多个写协程。

### 连接与传输 — [docs/claude/transport-limits.md](docs/claude/transport-limits.md)

- 四段超时各管连接生命的一段，`applyServerLimits` 是唯一配置点。**`ReadHeaderTimeout` 与 `IdleTimeout` 未设时回落到 `ReadTimeout`**，所以 `Debug()` 把读写放到 999s 时**不碰这两个** —— 否则每个进程都愿意花十六分钟收几百字节请求头（Slowloris 前提）。
- 请求头上限：**cookie 的量不由本站决定**（浏览器按域发，共享父域下的部署会连父域的一起收到，实测 12~16KB），所以默认值由框架给够（32KiB），配置只用来再往上抬 —— 逐份 config.json 去配等于每加一个服务就重新埋一次雷。**多跳部署里只有最小的那堵墙算数**，而 431 是在任何 handler 之前回的：`Connection: close` 无 `Date` = 这一跳拒的，带 `Date` = 上游拒的被转回来（反代已会点名是哪一跳）。

### 设了也没人读的选项 — [docs/claude/dead-options.md](docs/claude/dead-options.md)

- 本仓反复出现的形状：`WithX()` 写了一个**零读者**的字段，调用方没有任何诊断。已批量删掉六个（`WithIgnoreCase` 是默认带上还从没实现的那个）。删 `internal/acme/options.go` 顺带把整个 lego 库带出了依赖树。

### 日志 — [docs/claude/logging.md](docs/claude/logging.md)

- `IWriter` 收的是**一行已格式化**的字符串，且**按 logger** 挂 —— 想要"本进程的全部日志"，它给不了。用 `logger.AddSink(s)`：**进程级 + 结构化**，`Write` 必须同步且廉价（只许入队），`Wants(level)` 是第一道闸。
- ★ **比严重程度用 `Level.Severity()`，不要比 `Level` 的值** —— 数值不是单调的（`Info(5) < Warn(6) < Error(7) < Trace(8) < Debug(9)`）。

### 客户端 — [docs/claude/client.md](docs/claude/client.md)

- HttpClient 走 HTTPS 曾**一律 panic 在调用方的 goroutine 里**。ALPN 按能力读（`NegotiatedProtocolOf`）不按具体 TLS 类型断言；读不到就落到 HTTP/1.1，绝不"什么都不注册"。
- **配了代理不许静默直连** —— 所有分支统一走一个 `rawDial`；`tls.Client` 要手填 `ServerName` 和握手 deadline。
- 响应用**响应自己的** `Content-Type` 选 codec（回落到请求的），且必须走 `codec.IdentifyWireCodec`：那是对端填的字段。
- **重试默认关**（`RetryNever`），因为这套栈里跑着 sale / purchase / account / payment。只有 `RetryOnConnectError` 是可证安全的；`RetryOnError`（408/500）只给幂等读。

### 错误 — [docs/claude/errors.md](docs/claude/errors.md)

- 读错误码用 `errors.Code(err)` / `errors.From(err)`，**绝不用类型断言** —— 业务代码随手 `%w` 包一层，断言就漏，调用方拿到的 404 会变成 500。`(*Error).Is` 比的是 Id + Code + Detail，所以哨兵值过得了 JSON 往返。

### 注册中心与选点 — [docs/claude/registry-selector.md](docs/claude/registry-selector.md)

- 熔断：连续 `BreakerFailures`（默认 3）次失败踢出节点。**调用方必须两种结果都上报** `Mark`；反向代理只算连接级错误。全部节点熔断时**失败打开**。
- 一个服务名可能由**多个进程**在服务而端点集不同：合并按版本**加端点集指纹**（`registry.EndpointsKey`），选点按被调端点过滤（`selector.FilterEndpointIfKnown`，没人声明该端点时失败打开）。
- 注册中心驱动的路由更新：**只给 HTTP 端点建代理路由**（RPC 侧没有反向代理实现）；**本地路由永远赢过发现来的代理路由**（`hasLocalRoute` 那道盾）；服务下线的代理路由要**回收**，否则那条路径永远回 503。

### 消息总线 — [docs/claude/broker.md](docs/claude/broker.md)

- **每进程一个实例**：`broker.Use` 按解析后的后端名缓存。只有 server 那个拿到了真 registry、也只有它被 `Start()` 过 —— 第二个实例的 `Publish` 会遍历空节点列表、发不出去、**还返回 nil**。
- 广播与队列组的分野是一个**跨进程的字符串约定**，两边必须读同一处（`broadcastVersion()`）。
- 投递响应的状态码是发布方唯一的反馈通道：**4xx = 永久拒绝**（丢弃）、**5xx = 可重试**（留在积压里）、**2xx = 已投递**——包括 handler 报错的情况。
- 内置后端是**至多一次**，`Ack()` 是空操作，积压是发布方内存里的 64 条。`broker/redis` 把这些**全部反过来**：至少一次、`Ack()` 是真的 XACK、不用 registry —— 换后端就是换契约。

### 配置 — [docs/claude/config-tree.md](docs/claude/config-tree.md)

- **一个组件的配置是一个普通结构体；它拥有的孩子是字段，共享的东西是名字。** `config.Bind(path, ptr)` 是唯一入口，在 `newConfig` 末尾调一次；选项只写字段，不碰配置树。"拥有"的判据是**谁 Stop 它**。
- 是不是配置**由类型决定不由 tag 决定**：导出 + 非 interface/func/chan + 没有 `field:"-"`。`config/walk.go` 是所有路径（默认值、解码、热重载 diff、校验、lint、env）唯一的走图器。
- **文件仍然是只读输入**：`Bind` 只把当前值播进 defaults 层，`SaveToFile` **只落差异**，`Dump()` 才吐默认值。`${VAR}` 只在结构体上展开，永远不会被写回文件。
- 热重载走同一张图：解码进**深拷贝**、递归校验、只写真正变了的叶子；任何一层结构体都可以用 `IReloadable` 拒绝或接管。

### 已知问题与测试 — [docs/claude/known-issues.md](docs/claude/known-issues.md)、[docs/claude/testing.md](docs/claude/testing.md)

- 热重载读侧已同步：**热字段只有五个**（`router.Config.Recover` / `RequestPrinter`，`selector.Config` 的三个熔断参数），全部经访问器读，一次取一整组；`RWMutex` 的读锁**不可重入**。加锁前先看那个 Config 是不是早已整节 `ErrReloadUnsupported`。
- **go.mod 不许留 `replace`**（`TestGoMod_HasNoReplaceDirective` 盯着）。
- 测试：等外部事件**必须带超时**（挂起比失败更糟，它什么都不报告）；不要绑固定端口；**这台机器上 TCP 可达性探测不成立**（网络扩展全接受）；量代理量到的是 Clash 不是代理；★ **`httptest.ResponseRecorder` 与真服务器的判据是相反的** —— 头冻结只有真栈测得出，304 带正文只有 recorder 测得出。
- **先量了再叫它优化**（`router/handler_bench_test.go` 就是为一次"提速"其实慢了 15% 而存在的）。

### 出站：面对陌生人 — [docs/claude/outbound-ja3.md](docs/claude/outbound-ja3.md)

- `core/ja3` 是**独立 Go module**，blank import 才启用（主模块因此不依赖 utls）。**优先用预设名而不是 JA3 串** —— JA3 只记扩展 id 不记内容，手抄出来的是一个"谁也不是"的客户端。没导入子模块却设了指纹会返回 `ErrJa3NotRegistered`，绝不静默退回普通 TLS。
- `core/outbound` 打的是第三方站点（`client/` 打的是自己人）。**一个身份 = 指纹 + UA + 客户端提示 + 出口 IP，各自独立连接池**；**被拒不是传输错误**（403/429 要换身份并丢掉空闲连接）；**一个出口就只该有一个身份**，多出来的指纹当预备队，只在现役全在冷却时才上；**配了代理不许静默直连**（`allow_direct` 默认 false）。
