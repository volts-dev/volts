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

Execution order: global → route-level → embedded → main controller.

Because the framework serves HTTP and RPC from separate handler objects (the handler id includes `TransportType`), `Use` filters by transport **at assembly time**: the argument type carries the intent (`func(*THttpContext)` / `func(*TRpcContext)` / `func(IContext)`), and an `IMiddleware` may declare scope via the optional `IMiddlewareTransport`. Entries that do not apply are absent from the other side's `funcs` slice, so the cost is literally zero rather than merely small. **Any new cross-cutting feature must follow this rule — branch at registration, never in the request path.**

### Package Responsibilities

| Package | Responsibility |
|---|---|
| `router/` | Radix-tree routing, handler reflection, middleware pipeline, HTTP/RPC context, reverse proxy, static files |
| `server/` | Lifecycle management (Start/Stop), registry integration, broker subscriptions, transport wiring |
| `client/` | Outbound HTTP and RPC calls, service discovery via selector, retry logic |
| `transport/` | Wire-level HTTP and TCP transports, TLS/ACME, message framing with CRC32 |
| `codec/` | Serialization adapters (JSON via stdlib encoding/json, MsgPack, Protobuf, Gob, bytes) |
| `registry/` | Service registration/discovery — Consul, ETCD, mDNS, or in-memory backends |
| `broker/` | Pub/sub messaging — HTTP or in-memory backends |
| `selector/` | Load-balancing strategy over registry nodes, plus per-node failure ejection / circuit breaking (`selector/breaker.go`) |
| `logger/` | Self-contained leveled logger (writer manager + console writer). **Not** built on zap — `go.uber.org/zap` is pulled in only by `registry/etcd` for clientv3's `LogConfig` |
| `internal/` | Shared utilities (pool, metadata, TLS helpers, mDNS, ACME, backoff) |
| `config/` | Top-level config loading via `github.com/spf13/viper` |

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

### Registry-Driven Route Updates

`router/router.go:store()` is called on Registry watch events and periodic refresh (every 60s) to update proxy routes pointing to discovered services. Discovered nodes that resolve to **this process itself** are dropped by `filteSelf` (matched by `node.Id` **or** `addr.LocalFormat`-normalized address, so a same-address / different-id node from a `Debug()` port collision is still caught); otherwise a self-referential proxy route would replace the local handler and loop back into `127.0.0.1:<own port>` until ephemeral ports exhaust (`connect: can't assign requested address`). `router/reverse_proxy_loopguard.go` is the registry-independent runtime backstop: a hop-depth header caps any forwarding cycle, and a self-address short-circuit (via `http.LocalAddrContextKey`) refuses to dial the process's own listen port. That short-circuit matches loopback **and** any local NIC address — the registry stores NIC addresses (`192.168.x.x:35999`), and when `LocalFormat`'s fold-to-loopback misses (10s IP cache vs. a network switch, or a zombie node advertising a stale IP) a loopback-only check is bypassed entirely.

**Only HTTP endpoints get proxy routes.** `store()` skips `CONNECT` (RPC) endpoints: there is no RPC reverse-proxy implementation — `RpcReverseProxy` was an empty function body, so the route silently swallowed the request and the caller waited out `RequestTimeout` (20s) for a `context deadline exceeded` with nothing in the log pointing back here. Worse, `TTree.addNode` replaces same-path routes, so that empty handler would evict the local RPC handler and turn working local calls into black holes. Cross-process RPC dials the target node directly via `client` and never transits the gateway, so nothing needs proxying. A request for a service this process doesn't host now gets `StatusNotFound` immediately.

`registry.Service.Equal` decides (via `TRouter.isLocalService`) whether `refresh()` skips a service entirely, so a wrong `true` means **permanently no route**. It compares name, version, node count, and node ids — historically the whole comparison sat inside `if len(a.Nodes) == len(b.Nodes)` and fell through to `return true` when counts differed, which any zombie node (SIGKILL leaves the etcd lease un-revoked, so old+new nodes coexist and the count doubles) turned into "remote service misread as local". Keep the bias toward `false`: a false negative only costs one extra `store()` pass, where `filteSelf` filters precisely.

(The former `handler.Services`/`SetServices` field — once documented here as a data race — is gone: it had no readers and `SetServices` was never called, so the race could not occur. Removed rather than locked.)

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

- **config hot-reload is racy** (`config.WithWatcher()`): the watcher goroutine calls
  `UnmarshalKey(model.String(), model)`, which rewrites **every tagged field** of every
  registered config model in place, while request goroutines read those same fields
  (`router.Config.Recover` per request, `transport.Config.ReadTimeout` per connection,
  `selector.Config.BreakerFailures` per Select). vectors enables this in production.
  Deliberately left unfixed — a correct fix needs a config-layer redesign (copy-on-write
  + accessor reads), which is scheduled separately. Do not "just add a mutex": the readers
  are arbitrary business code touching struct fields directly.
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
- `transport/message_fuzz_test.go` holds the fuzz targets; deep runs with
  `gotip test -run=XXX -fuzz=FuzzMessageDecode -fuzztime=10m ./transport/`.
- **Measure before calling something an optimization.** `router/handler_bench_test.go`
  exists because a "hoist the type assertion out of the loop" change was committed as a
  perf win and turned out to be 15% *slower*. In short chains the surrounding ServeHTTP
  work (route match, context pool, ~19 allocs, ~2µs) drowns out anything the dispatch loop
  does — use the 50-middleware case, `-count=5`, and compare medians.

### Optional Submodule: JA3

`ja3/` is a **separate Go module** (`github.com/volts-dev/volts/ja3`) holding the TLS
fingerprint-spoofing implementation. The main module deliberately does not depend on it,
and therefore does not depend on `github.com/refraction-networking/utls` — a library whose
purpose is mimicking other clients' TLS fingerprints, which most people writing business
services do not need but would otherwise compile into every binary and have flagged in
compliance scans.

Enable it with a blank import:

```go
import _ "github.com/volts-dev/volts/ja3"
```

`transport` keeps only the hook (`transport/ja3_hook.go`): the `Ja3` config struct (plain
strings), a dialer registry, and `INegotiatedProtocol` so `client` can read the ALPN result
without asserting a concrete TLS type. Setting a JA3 fingerprint **without** importing the
submodule returns `transport.ErrJa3NotRegistered` rather than silently falling back to
ordinary TLS — a silent fallback would leave the caller believing they were disguised.

The JA3 integration tests live in the submodule (`ja3/client_integration_test.go`); they
cannot stay in the main module, since importing the submodule from there would both create
a module cycle and drag utls back into the main dependency tree.
