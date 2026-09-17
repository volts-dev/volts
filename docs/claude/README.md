# CLAUDE.md 的细节分册

[`../../CLAUDE.md`](../../CLAUDE.md) 只留每次都要知道的部分（命令、架构、约定、一份索引）
—— 它每个会话都会被整份读进上下文，所以那里放的是**规则**，这里放的是**来龙去脉**：判据、
真栈量到的数、坏掉时看起来像什么、回归测试在哪。

动到哪一块就把那一册读完再改。写新条目时正文进这里，同时在 CLAUDE.md 的索引里补一行。

| 分册 | 讲什么 |
|---|---|
| [router-http.md](router-http.md) | 不服务工作目录、静态文件与 304、响应压缩、延迟的 WriteHeader、WebSocket |
| [transport-limits.md](transport-limits.md) | 四段连接超时，以及 `Debug()` 为什么只许放宽其中两段 |
| [dead-options.md](dead-options.md) | 写了字段却没有读者的选项，两轮批量清理 |
| [logging.md](logging.md) | `IWriter` 与 `Sink` 的分工、`Level.Severity()`、调用点解析 |
| [client.md](client.md) | HTTPS 拨号（曾一律 panic）、代理、UA、响应编解码、重试策略 |
| [errors.md](errors.md) | `errors.Code` / `From` / `Wrap`，以及码为什么要穿得过包装 |
| [registry-selector.md](registry-selector.md) | 熔断、端点感知选点、注册中心驱动的代理路由与回收 |
| [broker.md](broker.md) | 每进程一个实例、广播约定、投递状态码的三种含义、redis 后端的反向契约 |
| [config-tree.md](config-tree.md) | 配置树＝结构体图：Bind、拥有 vs 共享、只落差异、热重载、校验与 lint |
| [known-issues.md](known-issues.md) | 两轮审计的留痕、热字段的读侧同步、go.mod 不许有 replace |
| [testing.md](testing.md) | 超时、端口、这台机器上不成立的探测、recorder 与真栈相反的判据 |
| [outbound-ja3.md](outbound-ja3.md) | `core/ja3` 子模块与 `core/outbound` 的身份、轮换、代理 |
