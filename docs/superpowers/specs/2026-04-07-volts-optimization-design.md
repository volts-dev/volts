# Volts 框架深度优化设计文档

**日期：** 2026-04-07  
**作者：** Johan  
**状态：** 已批准，待实施  
**方案：** A — 顺序修复（Critical → High → Medium）

---

## 一、目标

1. 修复全部 20 个已知 Bug（3 Critical、5 High、12 Medium）及代码探索中新发现的缺陷
2. 为 Critical + High 级别修复编写配套测试
3. 重写中英双语 README
4. 输出独立的技术栈重构设计文档

---

## 二、Bug 修复清单

### 🔴 Critical（3 个）— 必须写测试

| # | 文件 | 行号 | 问题描述 | 修复方案 |
|---|------|------|---------|---------|
| C1 | `router/reverse_proxy.go` | ~145 | WebSocket goroutine 泄漏：启动 2 个 goroutine 只等 1 个完成 | 改为等待 2 个 `<-errCh` |
| C2 | `router/router.go` | ~260 | Context Pool 竞态：double-checked locking 导致重复创建 pool | 改用 `sync.Map.LoadOrStore` 原子操作 |
| C3 | `volts.go` | ~126 | `signal.Notify` 包含 SIGKILL（系统无法捕获，静默失效） | 移除 SIGKILL，保留 SIGTERM/SIGINT/SIGQUIT |

### 🟠 High（5 个）— 必须写测试

| # | 文件 | 行号 | 问题描述 | 修复方案 |
|---|------|------|---------|---------|
| H1 | `router/router.go` | ~50 | `httpCtxPool`/`rpcCtxPool` 永不清理，路由动态增删时无限增长 | 路由删除时同步清理对应 pool key |
| H2 | `server/server.go` | ~487 | `registered` 字段无锁并发访问 | 改用 `atomic.Bool` |
| H3 | `server/server.go` | ~370 | Subscriber 清理设 nil 而非 delete，map 持续增长 | 改用 `delete(map, key)` |
| H4 | `router/router.go` | ~530 | context cancel 无法保证正确触发，goroutine 泄漏风险 | 确保 cancel 在所有代码路径均被调用 |
| H5 | `server/server.go` | ~75 | `Stop()` 只返回最后一个错误，中间错误被丢弃 | 用 `errors.Join`（Go 1.20+）聚合所有错误 |

### 🟡 Medium（12 个）— 无需测试，仅修复

| # | 文件 | 问题描述 | 修复方案 |
|---|------|---------|---------|
| M1 | `router/group.go` | `RegisterGroup` 并发 map 写入无保护 | 加 `sync.RWMutex` |
| M2 | `client/` | `IClient` 接口缺少 `Call`/`Publish` 方法 | 补充接口方法定义 |
| M3 | `router/router.go` | 多处类型断言无 ok 检查，panic 风险 | 改为 `v, ok := x.(T)` |
| M4 | `transport/tcp.go` | 位置计数器 `atomic.Int32` 溢出后未重置 | 加溢出检查或改用 `uint32` |
| M5 | `registry/` | 服务过滤 O(N×M) 复杂度 | 改用 map 做 O(1) 查找 |
| M6 | `router/handler.go` | handler `pos` 计数器 Reset 时可能越界 | 增加边界检查 |
| M7 | `codec/codec.go` | 未注册 codec 时返回 nil，调用方 panic | 返回明确错误或 fallback codec |
| M8 | `transport/message.go` | CRC32 校验失败时只打日志不返回错误 | 改为返回错误给调用方 |
| M9 | `server/server.go` | `Start()` 中 pprof 注册无并发保护（多实例时 panic） | 加 `sync.Once` |
| M10 | `router/tree.go` | 路由删除后 `Count` 计数可能为负 | 加 `max(0, count-1)` 保护 |
| M11 | `broker/http` | HTTP broker subscriber 无超时，长轮询永久阻塞 | 添加可配置超时 |
| M12 | `selector/` | 节点列表为空时 panic 而非返回错误 | 改为返回 `ErrNotFound` |

---

## 三、测试策略

仅对 Critical + High 级别修复编写测试：

| # | 测试文件 | 测试内容 | 验证手段 |
|---|---------|---------|---------|
| C1 | `router/reverse_proxy_test.go` | WebSocket 双向转发完成后无 goroutine 泄漏 | `runtime.NumGoroutine` 前后对比 |
| C2 | `router/router_test.go` | 100 goroutine 并发触发同一路由，pool 只创建一次 | `-race` 竞态检测器 |
| C3 | `volts_test.go` | 信号处理只注册 SIGTERM/SIGINT/SIGQUIT | 反射检查注册信号列表 |
| H1 | `router/router_test.go` | 动态添加再删除路由后，pool map 中对应 key 已清理 | `sync.Map` key 不存在断言 |
| H2 | `server/server_test.go` | 并发调用 `Register`/`Deregister` 不产生竞态 | `-race` |
| H3 | `server/server_test.go` | `Deregister` 后 subscriber map 的 key 完全移除 | map 长度断言 |
| H4 | `router/router_test.go` | 请求取消后，相关 goroutine 在 100ms 内退出 | goroutine 泄漏检测 |
| H5 | `server/server_test.go` | `Stop()` 多子系统失败时所有错误均包含在返回值中 | `errors.Is` 检查所有错误 |

---

## 四、执行顺序

```
阶段 1 — Critical 修复
  C3 volts.go (最简单，不影响其他文件)
  C2 router/router.go (pool 竞态)
  C1 router/reverse_proxy.go (WebSocket 泄漏)
  → go test -race ./... 全量验证

阶段 2 — High 修复
  H2 server/server.go (registered 字段)
  H3 server/server.go (subscriber 清理)
  H5 server/server.go (Stop 错误聚合)
  H1 router/router.go (pool 无限增长)
  H4 router/router.go (context cancel)
  → go test -race ./router/... ./server/...

阶段 3 — Medium 修复
  M9 server/server.go (pprof sync.Once)
  M1 router/group.go (RegisterGroup mutex)
  M3 router/router.go (类型断言)
  M6 router/handler.go (pos 越界)
  M10 router/tree.go (Count 负数)
  M7 codec/codec.go (nil codec)
  M8 transport/message.go (CRC32 错误)
  M4 transport/tcp.go (计数器溢出)
  M11 broker/http (长轮询超时)
  M12 selector/ (空节点 panic)
  M5 registry/ (O(N×M) 过滤)
  M2 client/ (IClient 接口)
  → go test ./...

阶段 4 — 交付物
  重写 README.md（中英双语）
  写 docs/superpowers/specs/2026-04-07-tech-stack-redesign.md
```

---

## 五、README 结构（中英双语）

```
标题 + 徽章（Build、Go Version、License）
简介（2-3 句话）
特性列表
架构图（ASCII 分层图）
快速开始
  └── 安装 / Hello World HTTP / Hello World RPC
核心概念
  └── Service 生命周期 / Router & 路由语法 / Middleware
      Transport / Codec / Registry / Broker
配置参考
性能说明
路线图（指向独立设计文档）
贡献指南 / License
```

每节中文在上、英文紧随其后，代码块只写一次。

---

## 六、技术栈设计文档结构

文件：`docs/superpowers/specs/2026-04-07-tech-stack-redesign.md`

```
1. 现状分析（当前依赖树、技术债、Bug 根因分类）
2. 短期升级建议（slog 替换 zap、errors.Join、减少外部依赖）
3. 中期重构建议（QUIC/HTTP3、FlatBuffers、Kubernetes Registry、OpenTelemetry）
4. 长期架构愿景（插件化、WASM 扩展点、io_uring、Service Mesh xDS）
5. 迁移路径与兼容性策略
```

---

## 七、成功标准

- `go test -race ./...` 全部通过
- `go vet ./...` 无新警告
- 8 个配套测试全部绿色
- README.md 中英双语，覆盖所有核心概念
- 技术栈设计文档已提交
