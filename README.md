# Volts

> A lightweight, interface-driven Go framework for building HTTP servers, RPC services, and event-driven microservices.

[![Go Version](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/license-MIT-blue)](LICENSE)
[![Module](https://img.shields.io/badge/module-github.com%2Fvolts--dev%2Fvolts-informational)](https://github.com/volts-dev/volts)

---

## Overview

Volts is a Go web + RPC framework built around a clean, layered interface architecture. It provides a single unified programming model that scales from simple HTTP endpoints to full microservice meshes — without forcing you to swap frameworks as your service grows.

**Key characteristics:**

- **Interface-first** — every major component (`IServer`, `IRouter`, `ITransport`, `IBroker`, `IRegistry`, `IClient`) is an interface. Swap implementations without touching business logic.
- **Options pattern throughout** — all configuration uses `func(*Config)` closures, keeping zero-value structs always valid.
- **Context pooling** — per-route `sync.Pool` maps recycle HTTP and RPC contexts on the hot path, keeping GC pressure minimal.
- **Single binary, multiple protocols** — HTTP, RPC over TCP, and pub/sub messaging coexist in the same process with the same router.

---

## Architecture

```
IService  (volts.go)
  ├── IServer      (server/)      — lifecycle, registry, broker wiring
  │     └── IRouter (router/)    — radix-tree routing, handler pipeline
  │           └── IContext        — THttpContext | TRpcContext
  ├── ITransport   (transport/)  — HTTP or TCP wire protocol, TLS, ACME
  ├── IRegistry    (registry/)   — service discovery backend
  ├── IBroker      (broker/)     — pub/sub messaging bus
  ├── IClient      (client/)     — outbound RPC / HTTP calls
  └── ISelector    (selector/)   — load-balancing over registry nodes
```

**Request flow**

```
Client Request
  → ITransport  (accept connection)
  → IServer     (pass to router)
  → IRouter     (radix-tree match)
  → handler chain (middleware → handler)
  → IContext    (write response back through transport)
```

---

## Installation

```bash
go get github.com/volts-dev/volts
```

Requires **Go 1.24+**.

---

## Quick Start

### HTTP Server

```go
package main

import (
    "github.com/volts-dev/volts"
    "github.com/volts-dev/volts/router"
    "github.com/volts-dev/volts/server"
)

func main() {
    r := router.New()

    r.Url("GET", "/", func(ctx *router.THttpContext) {
        ctx.Respond([]byte("Hello, Volts!"))
    })

    r.Url("GET", "/hello/<:name>", func(ctx *router.THttpContext) {
        name := ctx.PathParams().FieldByName("name").AsString()
        ctx.RespondByJson(map[string]string{"hello": name})
    })

    app := volts.New(
        volts.Server(server.New(server.WithRouter(r))),
    )
    app.Run()
}
```

### Controller (Struct-based)

```go
type UserCtrl struct{}

func (c UserCtrl) Get(ctx *router.THttpContext) {
    ctx.RespondByJson(map[string]any{"id": 1, "name": "Alice"})
}

func (c UserCtrl) Post(ctx *router.THttpContext) {
    // ctx.Body() / ctx.MethodParams()
}

func main() {
    r := router.New()
    r.Url("REST", "/users", new(UserCtrl)) // maps GET/POST/PUT/DELETE automatically
    volts.New(volts.Server(server.New(server.WithRouter(r)))).Run()
}
```

### Route Groups

```go
r := router.New()

v1 := router.NewGroup(router.WithGroupPathPrefix("/api/v1"))
v1.Url("GET", "/users", listUsers)
v1.Url("POST", "/users", createUser)

r.RegisterGroup(v1)
```

---

## RPC Server

Volts supports binary RPC over TCP using the `CONNECT` method. The same router handles both HTTP and RPC routes.

**Server**

```go
type Arith struct{}

func (a Arith) Mul(ctx *router.TRpcContext, req *Args, reply *Reply) error {
    reply.Num = req.Num1 * req.Num2
    return nil
}

func main() {
    r := router.New()
    r.Url("CONNECT", "Arith", new(Arith))

    app := volts.New(
        volts.Server(server.New(server.WithRouter(r))),
        volts.Transport(transport.NewTCPTransport()),
    )
    app.Run()
}
```

**Client**

```go
cli, _ := client.NewHttpClient()
req, _ := cli.NewRequest("Arith.Mul", "Test.Endpoint", &Args{Num1: 3, Num2: 4})
cli.Call(req, client.WithAddress("127.0.0.1:35999"))
```

---

## Publish / Subscribe

Volts includes a built-in broker abstraction for event-driven messaging. The same server that handles HTTP requests can also subscribe to topics.

```go
package main

import (
    "context"
    "github.com/volts-dev/volts/broker"
    _ "github.com/volts-dev/volts/broker/memory"
    "github.com/volts-dev/volts/router"
    "github.com/volts-dev/volts/server"
)

// 1. Define message
type OrderEvent struct{ OrderID string; Amount float64 }

func (e *OrderEvent) Topic() string        { return "order.created" }
func (e *OrderEvent) ContentType() string  { return "application/json" }
func (e *OrderEvent) Payload() interface{} { return e }

// 2. Subscribe
func onOrder(ctx *router.TSubscriberContext) {
    msg := ctx.Event.Message()
    // process msg.Body ...
}

func main() {
    r := router.New()
    r.Subscribe(r.NewSubscriber("order.created", onOrder))

    b := broker.Use("memory")
    srv := server.New(server.WithRouter(r), server.WithBroker(b))
    srv.Start()

    // 3. Publish
    pub := server.NewPublisher(server.WithPublisherBroker(b))
    pub.Publish(context.Background(), &OrderEvent{OrderID: "ORD-001", Amount: 99.9})
}
```

### Supported Brokers

| Backend | Import | Use Case |
|---|---|---|
| **Memory** | `broker/memory` | Local testing, single-process event fan-out |
| **HTTP** | `broker/http` | Cross-service pub/sub without a message broker |

---

## Middleware

Middleware is embedded in handler structs. The framework discovers lifecycle hooks (`Init`, `Before`, `After`, `Panic`) automatically.

```go
import "github.com/volts-dev/volts-middleware/event"

type UserCtrl struct {
    event.TEvent // embeds Before / After / Panic hooks
}

func (c UserCtrl) Before(ctx *router.THttpContext) {
    // runs before every method in UserCtrl
}

func (c UserCtrl) Get(ctx *router.THttpContext) {
    ctx.RespondByJson(map[string]any{"ok": true})
}
```

---

## Service Discovery & Registry

Register your service with a discovery backend so clients can find it automatically:

```go
import (
    "github.com/volts-dev/volts/registry/consul"
    "github.com/volts-dev/volts/registry/etcd"
    _ "github.com/volts-dev/volts/registry/mdns"  // zero-config LAN discovery
)

// Consul
app := volts.New(
    volts.Server(srv),
    volts.Registry(consul.New(consul.Address("127.0.0.1:8500"))),
)

// etcd
app := volts.New(
    volts.Server(srv),
    volts.Registry(etcd.New(etcd.Address("127.0.0.1:2379"))),
)
```

| Backend | Package | Notes |
|---|---|---|
| **mDNS** | `registry/mdns` | Zero-config, LAN-only |
| **Memory** | `registry/memory` | In-process, testing |
| **Consul** | `registry/consul` | Production-grade, health checks |
| **etcd** | `registry/etcd` | Kubernetes-native |

---

## Transport

```go
import "github.com/volts-dev/volts/transport"

// HTTP (default)
volts.Transport(transport.NewHTTPTransport(transport.WithAddrs(":8080")))

// TCP (for RPC)
volts.Transport(transport.NewTCPTransport())

// TLS
volts.Transport(transport.NewHTTPTransport(
    transport.WithAddrs(":443"),
    transport.WithTLSConfig(tlsCfg),
))
```

ACME / Let's Encrypt automatic certificate provisioning is supported via `internal/acme`.

---

## Codec

Message serialization is negotiated per request via `Content-Type`. All codecs are registered by content-type hash and selected automatically.

| Format | Content-Type | Notes |
|---|---|---|
| JSON (Sonic) | `application/json` | Default, SIMD-accelerated |
| MessagePack | `application/msgpack` | Compact binary |
| Protobuf | `application/protobuf` | Schema-based |
| Gob | `application/gob` | Go-native |
| Raw bytes | `application/bytes` | Zero-copy passthrough |

---

## Real-Time Web (SSE Demo)

The `demo/pubsub_web` example shows how to bridge the broker to browser clients using Server-Sent Events:

```
broker subscriber → Hub.Broadcast() → SSE /events → EventSource (JS)
```

```bash
go run ./demo/pubsub_web/cmd/
# open http://localhost:8080
```

The page renders a live dashboard updating every second — no WebSocket library needed.

---

## Project Layout

```
volts/
├── broker/          # pub/sub — memory & HTTP backends
├── client/          # outbound HTTP and RPC calls
├── codec/           # JSON/Sonic, MsgPack, Protobuf, Gob, bytes
├── config/          # top-level config loading (Viper)
├── demo/            # runnable examples
│   ├── web/         # HTTP: hello world, REST, middleware, module groups
│   ├── rpc/         # TCP RPC: server + client
│   ├── pubsub/      # broker: pub/sub with memory backend
│   └── pubsub_web/  # SSE: real-time browser dashboard
├── internal/        # shared utilities (pool, TLS, mDNS, ACME, backoff)
├── logger/          # structured logging (zap)
├── registry/        # service discovery — Consul, etcd, mDNS, memory
├── router/          # radix-tree routing, handler reflection, context pool
├── selector/        # load-balancing strategy over registry nodes
├── server/          # lifecycle, registry wiring, broker subscriptions
└── transport/       # HTTP and TCP transports, TLS, message framing
```

---

## Configuration

All configuration uses the options pattern. Every package exposes `New(opts ...Option)`.

```go
// Server options
server.New(
    server.Address(":8080"),
    server.WithName("my-service"),
    server.WithRouter(r),
    server.WithBroker(b),
    server.WithRegistry(reg),
    server.WithTransport(t),
    server.RegisterTTL(30 * time.Second),
    server.RegisterInterval(15 * time.Second),
)

// Router options
router.New(
    router.WithRoutesTreePrinter(), // print route table on start
    router.WithRequestPrinter(),    // log every request
    router.WithPprof(),             // mount /debug/pprof
)

// App lifecycle hooks
volts.New(
    volts.Server(srv),
    volts.BeforeStart(func() error { /* ... */ return nil }),
    volts.AfterStop(func() error  { /* ... */ return nil }),
)
```

---

## Service Lifecycle

```
app.Run()
  └─ app.Start()
       ├─ config.Load()
       ├─ transport.Listen()
       ├─ broker.Start()
       ├─ server.Register()  → registry + broker subscriptions
       └─ (loop) transport.Serve() → router → handler
  └─ wait for SIGINT / SIGTERM / SIGQUIT
  └─ app.Stop()
       ├─ server.Deregister()
       ├─ broker.Close()
       └─ transport.Close()
```

---

## Commands

```bash
# Build
go build ./...

# Run all tests
go test ./...

# Run with race detector
go test -race ./...

# Specific package
go test ./router/...
go test ./broker/memory/...

# Benchmarks
go test ./router/ -bench=. -benchmem

# Vet
go vet ./...
```

---

## Applicable Domains

Volts is designed to be a general-purpose infrastructure layer. Its interface-driven architecture makes it suitable wherever multiple protocols or deployment topologies need to coexist.

### Today

| Domain | How Volts fits |
|---|---|
| **Web APIs** | HTTP router with REST mapping, path params, middleware pipeline |
| **Internal RPC** | TCP transport with binary codec, service discovery, load balancing |
| **Event-driven services** | Broker abstraction with memory/HTTP backends, typed message handlers |
| **Microservice platforms** | Registry (Consul/etcd/mDNS), selector, per-service deployment |
| **Real-time dashboards** | SSE endpoint bridged from broker, proven by the pubsub_web demo |
| **Reverse proxy / gateway** | Built-in proxy handler (`router/reverse_proxy.go`) with WebSocket support |

### Near-term

| Domain | Path |
|---|---|
| **gRPC gateway** | Swap transport layer; codec already supports Protobuf |
| **IoT device backends** | TCP transport + binary codec suit constrained devices; mDNS enables zero-config LAN discovery |
| **WebSocket services** | Hijacker interface already present in THttpResponse; upgrade path is straightforward |
| **Multi-tenant SaaS** | Demonstrated by the vectors project built on top of volts |
| **NATS / Kafka broker adapters** | Implement IBroker to add cloud-native message buses without changing service code |
| **WASM edge compute** | Interface isolation means transports and registries can be replaced with edge-compatible implementations |

### Long-term

| Domain | Vision |
|---|---|
| **Service mesh data plane** | IRegistry + ISelector already encode the discovery/load-balancing contract; a sidecar proxy is a natural extension |
| **AI inference serving** | High-throughput binary RPC over TCP with Protobuf codec suits model serving endpoints |
| **Distributed workflow engines** | Broker pub/sub + registry form the coordination primitives; a durable task queue sits on top |
| **Plugin / module marketplaces** | The module group system (demonstrated in vectors) provides install/uninstall lifecycle per tenant |

---

## Contributing

Issues and pull requests are welcome. Please run `go vet ./...` and `go test -race ./...` before submitting.

**QQ Group:** 151120790

---

## License

MIT © volts-dev
