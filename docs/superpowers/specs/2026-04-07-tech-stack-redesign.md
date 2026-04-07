# Volts 技术栈重构设计文档
# Volts Tech Stack Redesign Design Document

**日期 / Date:** 2026-04-08  
**状态 / Status:** 建议草案 / Draft Proposal  
**作者 / Author:** Johan

---

## 1. 现状分析 / Current State Analysis

### 1.1 当前依赖树 / Current Dependencies

| 依赖 | 版本 | 用途 | 评估 |
|------|------|------|------|
| `bytedance/sonic` | v1.15.0 | JSON 编解码 | ✅ 保留，SIMD 加速，比标准库快 3-5x |
| `go.uber.org/zap` | v1.26.0 | 结构化日志 | ⚠️ 可替换为 `log/slog`（Go 1.21 标准库） |
| `github.com/spf13/viper` | latest | 配置加载 | ⚠️ 重量级，引入 15+ 间接依赖，可精简 |
| `go.etcd.io/etcd/client/v3` | v3.5.21 | etcd 注册 | ⚠️ 引入大量 gRPC 间接依赖 |
| `github.com/hashicorp/consul/api` | v1.32.0 | Consul 注册 | ✅ 保留，生产常用 |
| `github.com/miekg/dns` | v1.1.65 | mDNS | ✅ 保留，局域网发现 |
| `github.com/vmihailenco/msgpack/v5` | v5.4.1 | MessagePack | ✅ 保留 |
| `github.com/volts-dev/cacher` | latest | Handler 缓存 | 🔴 外部依赖存在竞态（container/list），需升级或替换 |

### 1.2 技术债根因分类 / Root Cause Classification

**并发模型问题（已修复）：**
- C1 WebSocket goroutine 泄漏 → 已修复
- C2 Context Pool double-checked locking → 已修复（LoadOrStore）
- H1 Pool 条目不清理 → 已修复
- H2 registered 字段竞态 → 已修复（atomic.Bool）

**接口设计问题（已改善）：**
- M2 IClient 接口方法缺失 → 已补充 `Call(IRequest) (IResponse, error)`
- IResponse 缺少 `StatusCode()` → 已补充

**错误处理问题（已修复）：**
- H5 Stop() 错误丢失 → 已用 `errors.Join` 聚合
- M8 CRC 校验失败只打日志 → 确认已 return error

**可观测性缺失（待解决）：**
- 无 OpenTelemetry 集成
- 日志分散，无统一 trace ID 传播
- 无 metrics 导出

**外部依赖竞态（待解决）：**
- `volts-dev/cacher` 的 `container/list` 存在并发写竞态（broker/http 测试发现）

---

## 2. 短期升级建议（0-3 个月）/ Short-term Upgrades (0-3 months)

> 不破坏现有 API，可作为 patch/minor 版本发布。

### 2.1 日志：zap → slog

**现状：** `go.uber.org/zap` v1.26，需维护包装层  
**建议：** 迁移至 Go 1.21 标准库 `log/slog`

```go
// 当前
log.Infof("Server started on %s", addr)

// 迁移后
slog.Info("server started", "addr", addr)
```

**收益：** 零外部依赖，API 稳定，结构化输出，可接入任意后端（JSON / text / OpenTelemetry）  
**工作量：** ~2 天，替换 `logger/` 包装层

### 2.2 错误处理：全面推广 errors.Join + %w

**现状：** 部分模块手动拼接错误字符串  
**建议：** 统一使用 `fmt.Errorf("%w", err)` 和 `errors.Join`

```go
// 当前（信息丢失）
return fmt.Errorf("failed: " + err.Error())

// 建议（可 errors.Is/As）
return fmt.Errorf("register service: %w", err)
```

**已完成：** `volts.go`、`server/server.go` 已更新  
**待完成：** `registry/`、`broker/` 包内部错误处理

### 2.3 修复 cacher 竞态

**现状：** `volts-dev/cacher` 的 `container/list` 在 `handlerManager.Put` 路径有并发写竞态  
**建议：** 

选项 A：将 `handlerManager` 的缓存层替换为内置 `sync.Pool`（无外部依赖）  
选项 B：向 cacher 提 PR 修复竞态  
选项 C：升级到 cacher 修复版本（如已发布）

**推荐选项 A**，彻底消除外部依赖风险：

```go
// 替换 cacher.StackCache 为 sync.Pool
type handlerManager struct {
    sync.RWMutex
    handlerPool  map[int]*sync.Pool  // 替换 sync.Map[int]cacher.StackCache
    handlerModel map[int]*handler
}
```

### 2.4 配置：精简 viper

**现状：** `config/` 包使用 `spf13/viper`，引入大量间接依赖  
**建议：** 对不需要热重载的场景，改用标准库 `encoding/json` + `os.Getenv`  
**兼容策略：** 保持现有 viper API 不变，在子包中提供轻量替代

---

## 3. 中期重构建议（3-12 个月）/ Mid-term Refactoring (3-12 months)

> 部分破坏 API，需要 minor 版本或独立子模块。

### 3.1 传输层：引入 QUIC/HTTP3

**库：** `github.com/quic-go/quic-go` v0.50+  
**实现：** 新增 `transport/quic/` 包，实现 `ITransport` 接口

```go
// 新增，不影响现有代码
import _ "github.com/volts-dev/volts/transport/quic"

app := volts.New(
    volts.Transport(quic.New(quic.WithAddrs(":443"))),
)
```

**收益：**
- 0-RTT 建链，连接延迟降低 30-50%
- 内置多路复用，消除 HTTP/1.1 队头阻塞
- 移动网络弱网下自动重连（QUIC 连接迁移）

**兼容性：** 新包，不修改现有 transport/

### 3.2 序列化：增加 FlatBuffers 支持

**库：** `github.com/google/flatbuffers/go`  
**实现：** 新增 `codec/flatbuffers.go`，注册到 codec registry

```go
import _ "github.com/volts-dev/volts/codec/flatbuffers"

// 使用 FlatBuffers（零拷贝解析）
r.Url("GET", "/stream", handler, codec.WithFlatBuffers())
```

**适用场景：** 游戏服务器、实时数据流、延迟敏感 RPC  
**收益：** 零内存分配解析，比 JSON 快 5-10x

### 3.3 服务发现：Kubernetes 原生 Backend

**实现：** 新增 `registry/kubernetes/` 包

```go
import _ "github.com/volts-dev/volts/registry/kubernetes"

app := volts.New(
    volts.Registry(kubernetes.New()), // 自动读取 in-cluster 配置
)
```

**依赖：** `k8s.io/client-go`（子模块，不影响主模块依赖）  
**收益：** 云原生部署无需额外注册中间件（Consul/etcd）

### 3.4 可观测性：集成 OpenTelemetry

**库：** `go.opentelemetry.io/otel` v1.x  
**集成点：**

```
Router middleware     → 注入 trace span，传播 W3C TraceContext header
Transport 层          → 读写 traceparent header
Registry 操作         → 记录注册/发现 metrics（计数、延迟）
Broker publish/sub    → span 跨进程传播
```

**实现方式：** 新增 `middleware/otel/` 中间件，可选引入

```go
import "github.com/volts-dev/volts/middleware/otel"

r.RegisterMiddleware(otel.Tracer())
r.RegisterMiddleware(otel.Metrics())
```

**导出：** stdout（开发）/ Jaeger（测试）/ Prometheus（生产）

---

## 4. 长期架构愿景（1-3 年）/ Long-term Vision (1-3 years)

### 4.1 插件化架构 / Plugin Architecture

每个子系统（Transport、Registry、Broker、Codec）通过标准接口作为独立插件：

```
volts.yaml:
  transport:
    type: quic
    version: "0.50"
  registry:
    type: kubernetes
  broker:
    type: nats
    addr: nats://localhost:4222
```

**实现路径：**  
1. 每个子系统独立 Go 子模块（`go.work` 工作区）
2. 主模块通过接口依赖，插件通过 `init()` 注册
3. 可选：`plugin.Open()` 热加载（Linux only）

### 4.2 WASM 扩展点 / WASM Extension Points

**场景：** 允许第三方以 WASM 模块形式提供 Handler，实现沙箱化执行

```go
// WASM Handler
r.Url("GET", "/wasm-handler", wasm.Load("handler.wasm"))
```

**运行时：** `github.com/tetratelabs/wazero`（纯 Go，零 CGO）  
**用途：** 多语言 handler、边缘计算、插件市场

### 4.3 基于 io_uring 的高性能 I/O（Linux）/ io_uring I/O (Linux)

**库：** `github.com/pawelgaczynski/gain`（基于 io_uring 的 Go 网络框架）  
**前提：** Linux kernel 5.1+  
**实现：** 新增 `transport/uring/`，build tag `//go:build linux`

```go
//go:build linux
import _ "github.com/volts-dev/volts/transport/uring"

app := volts.New(
    volts.Transport(uring.New()), // 仅 Linux 生效
)
```

**收益：** 高并发场景下 I/O 延迟降低 30-50%，syscall 数量减少

### 4.4 Service Mesh 集成 / Service Mesh Integration

**协议：** xDS/Envoy EDS（gRPC 流式）  
**实现：** 新增 `registry/xds/` 包

```go
import _ "github.com/volts-dev/volts/registry/xds"

app := volts.New(
    volts.Registry(xds.New(xds.ControlPlane("grpc://istiod:15010"))),
)
```

**收益：** 与 Istio/Linkerd/Envoy 生态互通，实现零代码侵入的流量治理

---

## 5. 迁移路径与兼容性策略 / Migration Path & Compatibility

### 5.1 版本语义

| 变更类型 | 版本策略 |
|----------|---------|
| Bug 修复、日志替换、错误处理改善 | patch (v0.x.y) |
| 新增 codec/transport/registry 实现 | minor (v0.x.0) |
| IClient/IResponse 接口扩展 | minor，向后兼容（嵌套接口） |
| 破坏性 API 变更（如删除字段） | major (v1.0.0) |

### 5.2 子模块策略

重型依赖（etcd、Kubernetes client、quic-go）放入独立子模块，主模块保持轻量：

```
volts/                    ← 主模块，零重型依赖
volts/registry/etcd/      ← 独立子模块
volts/registry/kubernetes/← 独立子模块
volts/transport/quic/     ← 独立子模块
volts/middleware/otel/    ← 独立子模块
```

### 5.3 向后兼容原则

1. **接口只增不减：** 新增方法通过嵌套接口提供，旧实现自动满足旧接口
2. **Options 模式保护：** 所有新配置通过新 Option 函数添加，不修改现有 Option 签名
3. **新包不修改旧包：** 新 backend 以新包形式提供，不触碰现有包代码
4. **废弃而非删除：** 需要移除的 API 先标注 `// Deprecated:`，下个 major 版本再删除

---

## 6. 优先级总结 / Priority Summary

| 优先级 | 建议 | 工作量 | 风险 |
|--------|------|--------|------|
| 🔴 立即 | 修复 cacher 竞态（替换为 sync.Pool） | 1-2 天 | 低 |
| 🟠 短期 | zap → slog | 2 天 | 低 |
| 🟠 短期 | 全面推广 errors.Join + %w | 1 天 | 低 |
| 🟡 中期 | OpenTelemetry 中间件 | 1-2 周 | 中 |
| 🟡 中期 | QUIC/HTTP3 Transport | 1-2 周 | 中 |
| 🟢 长期 | Kubernetes Registry | 1 周 | 低（新包） |
| 🟢 长期 | FlatBuffers Codec | 3 天 | 低（新包） |
| ⚪ 愿景 | WASM 扩展点 | 2-4 周 | 高 |
| ⚪ 愿景 | io_uring Transport | 2-3 周 | 高（平台限制） |
| ⚪ 愿景 | Service Mesh xDS | 3-4 周 | 高 |
