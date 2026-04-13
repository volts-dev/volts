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

**Codec dispatch** — `codec/codec.go` registers codecs by content-type hash. The transport `message.go` carries a codec identifier in the wire frame; the server/client negotiate serialization format (JSON via Sonic, MessagePack, Protobuf, Gob, or raw bytes).

### Package Responsibilities

| Package | Responsibility |
|---|---|
| `router/` | Radix-tree routing, handler reflection, middleware pipeline, HTTP/RPC context, reverse proxy, static files |
| `server/` | Lifecycle management (Start/Stop), registry integration, broker subscriptions, transport wiring |
| `client/` | Outbound HTTP and RPC calls, service discovery via selector, retry logic |
| `transport/` | Wire-level HTTP and TCP transports, TLS/ACME, message framing with CRC32 |
| `codec/` | Serialization adapters (JSON/Sonic, MsgPack, Protobuf, Gob, bytes) |
| `registry/` | Service registration/discovery — Consul, ETCD, mDNS, or in-memory backends |
| `broker/` | Pub/sub messaging — HTTP or in-memory backends |
| `selector/` | Load-balancing strategy over registry nodes |
| `logger/` | Structured logging built on `go.uber.org/zap` |
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

### Registry-Driven Route Updates

`router/handler.go:handler.Services` stores remote service nodes for proxy routes. It is written in two places — `generateHandler()` (no lock) and `SetServices()` (with lock) — which is a known data race. `router/router.go:store()` is called on Registry watch events and periodic refresh (every 60s) to update proxy routes pointing to discovered services.

### Known Issues (tracked in memory)

See project memory for 20 identified concurrency/correctness issues including:
- WebSocket goroutine leak in `router/reverse_proxy.go:145`
- Context pool race condition in `router/router.go:260`
- `SIGKILL` in signal handler (uncatchable) in `volts.go:130` — already fixed in current code
- Unprotected `registered` field in `server/server.go:487`
- `handler.Services` written without lock in `router/handler.go:207` (data race with `SetServices`)
