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
| `broker/` | Pub/sub messaging — HTTP or in-memory backends |
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

Still true after those fixes, and the reason to pick topics carefully: delivery is
**at-most-once** (`event.Ack()` is a no-op and `SubscribeConfig.AutoAck` is unimplemented),
the retry backlog is 64 messages in the publisher's memory and truncates the *newest* first,
`Publish` is fire-and-forget and never checks the response status, and
`httpSubscriber.Topic()` / `Config()` / `Unsubscribe()` are stubs (a subscription cannot be
cancelled). Use it for cache-invalidation-shaped signals — the message carries a key and the
receiver re-reads the source of truth — not for messages that must not be lost.

### Registry-Driven Route Updates

`router/router.go:store()` is called on Registry watch events and periodic refresh (every 60s) to update proxy routes pointing to discovered services. Discovered nodes that resolve to **this process itself** are dropped by `filteSelf` (matched by `node.Id` **or** `addr.LocalFormat`-normalized address, so a same-address / different-id node from a `Debug()` port collision is still caught); otherwise a self-referential proxy route would replace the local handler and loop back into `127.0.0.1:<own port>` until ephemeral ports exhaust (`connect: can't assign requested address`). `router/reverse_proxy_loopguard.go` is the registry-independent runtime backstop: a hop-depth header caps any forwarding cycle, and a self-address short-circuit (via `http.LocalAddrContextKey`) refuses to dial the process's own listen port. That short-circuit matches loopback **and** any local NIC address — the registry stores NIC addresses (`192.168.x.x:35999`), and when `LocalFormat`'s fold-to-loopback misses (10s IP cache vs. a network switch, or a zombie node advertising a stale IP) a loopback-only check is bypassed entirely.

**Only HTTP endpoints get proxy routes.** `store()` skips `CONNECT` (RPC) endpoints: there is no RPC reverse-proxy implementation — `RpcReverseProxy` was an empty function body, so the route silently swallowed the request and the caller waited out `RequestTimeout` (20s) for a `context deadline exceeded` with nothing in the log pointing back here. Worse, `TTree.addNode` replaces same-path routes, so that empty handler would evict the local RPC handler and turn working local calls into black holes. Cross-process RPC dials the target node directly via `client` and never transits the gateway, so nothing needs proxying. A request for a service this process doesn't host now gets `StatusNotFound` immediately.

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

