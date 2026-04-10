# 设计文档：同时支持 IP 和 localhost 访问服务器

**日期**: 2026-04-10  
**状态**: 已批准  
**仓库**: github.com/volts-dev/volts

---

## 问题描述

使用 IP 地址访问 HTTP 服务器失败，使用 `localhost` 则正常。

根本原因有两个相互叠加的 bug：

### Bug 1：`Address()` option 字段不同步

`server/config.go` 的 `Address()` option 只设置了 `Transport.Addrs`，未设置 `cfg.Address`：

```go
func Address(addr string) Option {
    return func(cfg *Config) {
        cfg.Transport.Config().Init(transport.WithAddrs(addr))
        // ❌ cfg.Address 从未赋值
    }
}
```

`Start()` 调用 `Listen(cfg.Address)`，`cfg.Address` 为空，依赖 fallback 到 `Transport.Addrs`。两字段不同步是潜在隐患。

### Bug 2：回环地址只绑定回环接口（核心原因）

Go 的 `net.Listen("tcp", "localhost:PORT")` 和 `net.Listen("tcp", "127.0.0.1:PORT")` 只绑定 `127.0.0.1` 回环接口，外部 IP 无法访问。

当地址被配置为 `localhost:PORT`（通过代码或配置文件），服务器只接受本机回环连接，局域网 IP 和外部 IP 全部失败。

---

## 解决方案（方案 A）

### 改动 1：`internal/net/net.go` — 监听地址归一化

在 `Listen()` 函数入口增加 `normalizeBindAddr()` 归一化逻辑：

- `localhost:PORT` → `:PORT`（绑定所有接口）
- `127.0.0.1:PORT` → `:PORT`
- `::1:PORT` → `:PORT`
- 其他地址原样保留

所有调用 `vnet.Listen` 的传输层（HTTP/TCP transport）自动受益。

### 改动 2：`server/config.go` — `Address()` option 字段同步

修复 `Address()` option，同时设置 `cfg.Address` 和 `Transport.Addrs`，确保两字段一致。

---

## 架构影响

- 不引入新接口，不改变公共 API
- `normalizeBindAddr` 是 `internal/net` 包内部函数，不对外暴露
- 现有使用 `:PORT` 的代码行为不变
- 明确指定非回环 IP（如 `192.168.1.100:PORT`）的代码行为不变

---

## 涉及文件

| 文件 | 改动 |
|------|------|
| `internal/net/net.go` | 新增 `normalizeBindAddr()`，在 `Listen()` 入口调用 |
| `server/config.go` | `Address()` option 增加 `cfg.Address = addr` |

---

## 测试要求

- 验证 `localhost:PORT` 地址可被本机 IP 访问
- 验证 `127.0.0.1:PORT` 地址可被本机 IP 访问
- 验证 `:PORT` 地址行为不变
- 验证显式非回环 IP 地址不被归一化
