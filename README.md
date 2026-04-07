# Volts

> 一个轻量、接口驱动的 Go 框架，用于构建 HTTP 服务器、RPC 服务和事件驱动微服务。
>
> A lightweight, interface-driven Go framework for building HTTP servers, RPC services, and event-driven microservices.

[![Go Version](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/license-MIT-blue)](LICENSE)
[![Module](https://img.shields.io/badge/module-github.com%2Fvolts--dev%2Fvolts-informational)](https://github.com/volts-dev/volts)

---

## 简介 / Overview

Volts 是一个围绕清晰分层接口架构构建的 Go Web + RPC 框架。它提供统一的编程模型，从简单 HTTP 端点到完整微服务网格均适用，无需随着服务增长而切换框架。

**核心特性 / Key characteristics:**

- **接口优先 / Interface-first** — 每个主要组件（`IServer`、`IRouter`、`ITransport`、`IBroker`、`IRegistry`、`IClient`）均为接口，可随时替换实现而不影响业务逻辑。
- **Options 模式 / Options pattern throughout** — 所有配置使用 `func(*Config)` 闭包，零值结构始终有效。
- **Context 对象池 / Context pooling** — 每条路由维护独立 `sync.Pool`，在热路径上复用 HTTP 和 RPC 上下文，将 GC 压力降到最低。
- **单进程多协议 / Single binary, multiple protocols** — HTTP、TCP RPC 和发布订阅消息在同一进程、同一路由器中共存。

---

## 架构 / Architecture

```
IService  (volts.go)
  ├── IServer      (server/)      — 生命周期、注册、broker 接入 / lifecycle, registry, broker wiring
  │     └── IRouter (router/)    — Radix Tree 路由、Handler 流水线 / radix-tree routing, handler pipeline
  │           └── IContext        — THttpContext | TRpcContext
  ├── ITransport   (transport/)  — HTTP 或 TCP 传输层、TLS、ACME / HTTP or TCP wire protocol, TLS, ACME
  ├── IRegistry    (registry/)   — 服务发现后端 / service discovery backend
  ├── IBroker      (broker/)     — 发布/订阅消息总线 / pub/sub messaging bus
  ├── IClient      (client/)     — 出站 RPC / HTTP 调用 / outbound RPC / HTTP calls
  └── ISelector    (selector/)   — 注册节点负载均衡 / load-balancing over registry nodes
```

**请求流程 / Request flow**

```
Client Request
  → ITransport  （接受连接 / accept connection）
  → IServer     （传递给路由器 / pass to router）
  → IRouter     （Radix Tree 匹配 / radix-tree match）
  → Handler 链  （中间件 → 处理器 / middleware → handler）
  → IContext    （通过传输层写回响应 / write response back through transport）
```

---

## 安装 / Installation

```bash
go get github.com/volts-dev/volts
```

需要 **Go 1.24+** / Requires **Go 1.24+**.

---

## 快速开始 / Quick Start

### HTTP 服务 / HTTP Server

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

### 结构体控制器 / Controller (Struct-based)

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
    r.Url("REST", "/users", new(UserCtrl)) // 自动映射 GET/POST/PUT/DELETE / maps automatically
    volts.New(volts.Server(server.New(server.WithRouter(r)))).Run()
}
```

### 路由分组 / Route Groups

```go
r := router.New()

v1 := router.NewGroup(router.WithGroupPathPrefix("/api/v1"))
v1.Url("GET",  "/users", listUsers)
v1.Url("POST", "/users", createUser)

r.RegisterGroup(v1)
```

---

## RPC 服务 / RPC Server

Volts 通过 `CONNECT` 方法支持基于 TCP 的二进制 RPC，同一路由器同时处理 HTTP 和 RPC 路由。

Volts supports binary RPC over TCP using the `CONNECT` method. The same router handles both HTTP and RPC routes.

**服务端 / Server**

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

**客户端 / Client**

```go
cli, _ := client.NewRpcClient()
req, _ := cli.NewRequest("Arith", "Mul", &Args{Num1: 3, Num2: 4})
resp, err := cli.Call(req, client.WithAddress("127.0.0.1:35999"))
```

---

## 发布/订阅 / Publish / Subscribe

Volts 内置 broker 抽象，支持事件驱动消息传递。处理 HTTP 请求的同一服务器也可以订阅主题。

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

// 1. 定义消息 / Define message
type OrderEvent struct{ OrderID string; Amount float64 }

func (e *OrderEvent) Topic() string        { return "order.created" }
func (e *OrderEvent) ContentType() string  { return "application/json" }
func (e *OrderEvent) Payload() interface{} { return e }

// 2. 订阅 / Subscribe
func onOrder(ctx *router.TSubscriberContext) {
    msg := ctx.Event.Message()
    // 处理消息 / process msg.Body ...
}

func main() {
    r := router.New()
    r.Subscribe(r.NewSubscriber("order.created", onOrder))

    b := broker.Use("memory")
    srv := server.New(server.WithRouter(r), server.WithBroker(b))
    srv.Start()

    // 3. 发布 / Publish
    pub := server.NewPublisher(server.WithPublisherBroker(b))
    pub.Publish(context.Background(), &OrderEvent{OrderID: "ORD-001", Amount: 99.9})
}
```

**支持的 Broker / Supported Brokers**

| 后端 / Backend | 导入 / Import | 适用场景 / Use Case |
|---|---|---|
| **Memory** | `broker/memory` | 本地测试、单进程事件扇出 / Local testing, single-process event fan-out |
| **HTTP** | `broker/http` | 跨服务发布订阅，无需消息中间件 / Cross-service pub/sub without a message broker |

---

## 中间件 / Middleware

中间件以结构体嵌入方式注入控制器，框架自动发现生命周期钩子（`Init`、`Before`、`After`、`Panic`）。

Middleware is embedded in handler structs. The framework discovers lifecycle hooks (`Init`, `Before`, `After`, `Panic`) automatically.

```go
import "github.com/volts-dev/volts-middleware/event"

type UserCtrl struct {
    event.TEvent // 嵌入 Before / After / Panic 钩子 / embeds lifecycle hooks
}

func (c UserCtrl) Before(ctx *router.THttpContext) {
    // 在 UserCtrl 每个方法前执行 / runs before every method in UserCtrl
}

func (c UserCtrl) Get(ctx *router.THttpContext) {
    ctx.RespondByJson(map[string]any{"ok": true})
}
```

---

## 服务发现 & 注册 / Service Discovery & Registry

将服务注册到发现后端，使客户端自动定位：

Register your service with a discovery backend so clients can find it automatically:

```go
import (
    "github.com/volts-dev/volts/registry/consul"
    "github.com/volts-dev/volts/registry/etcd"
    _ "github.com/volts-dev/volts/registry/mdns"  // 零配置局域网发现 / zero-config LAN discovery
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

| 后端 / Backend | 包 / Package | 备注 / Notes |
|---|---|---|
| **mDNS** | `registry/mdns` | 零配置，仅局域网 / Zero-config, LAN-only |
| **Memory** | `registry/memory` | 进程内，用于测试 / In-process, testing |
| **Consul** | `registry/consul` | 生产级，支持健康检查 / Production-grade, health checks |
| **etcd** | `registry/etcd` | Kubernetes 原生 / Kubernetes-native |

---

## 传输层 / Transport

```go
import "github.com/volts-dev/volts/transport"

// HTTP（默认 / default）
volts.Transport(transport.NewHTTPTransport(transport.WithAddrs(":8080")))

// TCP（RPC 场景 / for RPC）
volts.Transport(transport.NewTCPTransport())

// TLS
volts.Transport(transport.NewHTTPTransport(
    transport.WithAddrs(":443"),
    transport.WithTLSConfig(tlsCfg),
))
```

通过 `internal/acme` 支持 ACME / Let's Encrypt 自动证书签发。

ACME / Let's Encrypt automatic certificate provisioning is supported via `internal/acme`.

---

## 编解码 / Codec

消息序列化通过 `Content-Type` 按请求协商，所有 codec 以内容类型哈希注册并自动选择。

Message serialization is negotiated per request via `Content-Type`. All codecs are registered by content-type hash and selected automatically.

| 格式 / Format | Content-Type | 备注 / Notes |
|---|---|---|
| JSON (Sonic) | `application/json` | 默认，SIMD 加速 / Default, SIMD-accelerated |
| MessagePack | `application/msgpack` | 紧凑二进制 / Compact binary |
| Protobuf | `application/protobuf` | 基于 Schema / Schema-based |
| Gob | `application/gob` | Go 原生 / Go-native |
| Raw bytes | `application/bytes` | 零拷贝直通 / Zero-copy passthrough |

---

## 项目结构 / Project Layout

```
volts/
├── broker/          # 发布订阅 — Memory & HTTP 后端 / pub/sub — memory & HTTP backends
├── client/          # 出站 HTTP 和 RPC 调用 / outbound HTTP and RPC calls
├── codec/           # JSON/Sonic, MsgPack, Protobuf, Gob, bytes
├── config/          # 顶层配置加载 (Viper) / top-level config loading (Viper)
├── demo/            # 可运行示例 / runnable examples
│   ├── web/         # HTTP: hello world, REST, middleware, module groups
│   ├── rpc/         # TCP RPC: server + client
│   ├── pubsub/      # broker: pub/sub with memory backend
│   └── pubsub_web/  # SSE: real-time browser dashboard
├── internal/        # 共享工具（pool, TLS, mDNS, ACME, backoff）/ shared utilities
├── logger/          # 结构化日志 (zap) / structured logging (zap)
├── registry/        # 服务发现 — Consul, etcd, mDNS, memory / service discovery
├── router/          # Radix Tree 路由、Handler 反射、Context 池 / routing, handler, context pool
├── selector/        # 注册节点负载均衡策略 / load-balancing strategy over registry nodes
├── server/          # 生命周期、注册接入、broker 订阅 / lifecycle, registry, broker subscriptions
└── transport/       # HTTP 和 TCP 传输层、TLS、消息帧 / HTTP and TCP transports, TLS, message framing
```

---

## 配置 / Configuration

所有配置使用 Options 模式，每个包都暴露 `New(opts ...Option)`。

All configuration uses the options pattern. Every package exposes `New(opts ...Option)`.

```go
// 服务器选项 / Server options
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

// 路由器选项 / Router options
router.New(
    router.WithRoutesTreePrinter(), // 启动时打印路由表 / print route table on start
    router.WithRequestPrinter(),    // 记录每个请求 / log every request
    router.WithPprof(),             // 挂载 /debug/pprof
)

// 应用生命周期钩子 / App lifecycle hooks
volts.New(
    volts.Server(srv),
    volts.BeforeStart(func() error { /* ... */ return nil }),
    volts.AfterStop(func() error  { /* ... */ return nil }),
)
```

---

## 服务生命周期 / Service Lifecycle

```
app.Run()
  └─ app.Start()
       ├─ config.Load()
       ├─ transport.Listen()
       ├─ broker.Start()
       ├─ server.Register()  → 注册到 registry + broker 订阅 / registry + broker subscriptions
       └─ (循环 / loop) transport.Serve() → router → handler
  └─ 等待 SIGINT / SIGTERM / SIGQUIT / wait for OS signal
  └─ app.Stop()
       ├─ server.Deregister()
       ├─ broker.Close()
       └─ transport.Close()
```

---

## 常用命令 / Commands

```bash
# 构建 / Build
go build ./...

# 运行所有测试 / Run all tests
go test ./...

# 竞态检测 / Run with race detector
go test -race ./...

# 特定包 / Specific package
go test ./router/...
go test ./broker/memory/...

# 基准测试 / Benchmarks
go test ./router/ -bench=. -benchmem

# 静态检查 / Vet
go vet ./...
```

---

## 适用领域 / Applicable Domains

Volts 设计为通用基础设施层，其接口驱动架构使其适用于需要多协议或多部署拓扑并存的场景。

Volts is designed to be a general-purpose infrastructure layer. Its interface-driven architecture makes it suitable wherever multiple protocols or deployment topologies need to coexist.

### 当前 / Today

| 领域 / Domain | Volts 适配方式 / How Volts fits |
|---|---|
| **Web API** | HTTP 路由器，REST 映射，路径参数，中间件流水线 / HTTP router with REST mapping, path params, middleware pipeline |
| **内部 RPC / Internal RPC** | TCP 传输层，二进制 codec，服务发现，负载均衡 / TCP transport with binary codec, service discovery, load balancing |
| **事件驱动服务 / Event-driven services** | Broker 抽象，Memory/HTTP 后端，类型化消息处理器 / Broker abstraction with memory/HTTP backends |
| **微服务平台 / Microservice platforms** | Registry (Consul/etcd/mDNS)，Selector，每服务独立部署 / Registry, selector, per-service deployment |
| **实时仪表盘 / Real-time dashboards** | SSE 端点桥接 broker，pubsub_web demo 已验证 / SSE endpoint bridged from broker |
| **反向代理/网关 / Reverse proxy / gateway** | 内置代理处理器，支持 WebSocket / Built-in proxy handler with WebSocket support |

### 近期 / Near-term

| 领域 / Domain | 路径 / Path |
|---|---|
| **gRPC 网关 / gRPC gateway** | 替换传输层；codec 已支持 Protobuf / Swap transport layer; codec already supports Protobuf |
| **IoT 设备后端 / IoT device backends** | TCP 传输层 + 二进制 codec；mDNS 支持零配置局域网发现 / TCP transport + binary codec; mDNS enables zero-config LAN discovery |
| **WebSocket 服务 / WebSocket services** | THttpResponse 已实现 Hijacker 接口，升级路径简单 / Hijacker interface already present; upgrade path is straightforward |
| **NATS / Kafka Broker 适配器** | 实现 IBroker 接口即可接入云原生消息总线 / Implement IBroker to add cloud-native message buses |

### 长期愿景 / Long-term

| 领域 / Domain | 愿景 / Vision |
|---|---|
| **Service Mesh 数据面 / Service mesh data plane** | IRegistry + ISelector 已编码发现/负载均衡契约；边车代理是自然延伸 / IRegistry + ISelector already encode the discovery contract |
| **AI 推理服务 / AI inference serving** | 高吞吐 TCP RPC + Protobuf codec 适合模型服务端点 / High-throughput binary RPC over TCP with Protobuf codec suits model serving |
| **插件市场 / Plugin marketplaces** | Module Group 系统提供每租户安装/卸载生命周期 / The module group system provides install/uninstall lifecycle per tenant |

---

## 路线图 / Roadmap

详细技术栈升级建议请参阅：[技术栈重构设计文档](docs/superpowers/specs/2026-04-07-tech-stack-redesign.md)

For detailed tech stack upgrade recommendations, see: [Tech Stack Redesign Design Document](docs/superpowers/specs/2026-04-07-tech-stack-redesign.md)

---

## 贡献 / Contributing

欢迎 Issue 和 Pull Request，提交前请运行 `go vet ./...` 和 `go test -race ./...`。

Issues and pull requests are welcome. Please run `go vet ./...` and `go test -race ./...` before submitting.

**QQ Group:** 151120790

---

## License

MIT
