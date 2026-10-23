# 注册记录瘦身：服务描述按哈希寻址，留在所属进程的内存里

状态：已上线（2026-10-23），真栈结果见第 6 节。

## 1. 为什么

2026-10-23 本机实测（308 条注册记录，8.57MB）：

| 部分 | 大小 |
|---|---|
| 端点表 | 7.42 MB（87%） |
| 服务级 metadata | 1.02 MB |
| 节点信息（含签名） | 0.10 MB |

- 154 个服务的 http 记录与 rpc 记录，**除节点外逐字节相同**；去重后的描述只有 4.23MB。
- 描述只随二进制变，节点随进程变。两者绑在同一条记录里，于是一个进程重启（只是换了节点 id），
  所有读注册中心的进程都要把整份描述重新传、重新解码、重新解析：connect_components 在一次
  kylin + websvc 重启的 30 分钟里分配 11.9GB，其中服务索引反复重建 8.1GB。
- 单次重建的实测开销：etcd 整段读 13.2MB、解码全部记录 22.5MB、`BuildServiceIndex` 4.4MB。

## 2. 设计

**注册中心只放「谁在、在哪、描述是哪一份」；描述本身留在注册它的进程内存里，别人按哈希来取，
取到后校验哈希、解一次、缓存到进程结束。**

### 2.1 记录形状

注册时，`registry.Use()` 返回的后端外面包着一层描述解析器（`registry/resolver.go`）。
它把服务的 Metadata / Endpoints 剥下来，换成一个 metadata 键：

```
写进后端的记录 = {name, version, metadata: {"volts.desc": "<sha256 hex>"}, endpoints: [], nodes: [...]}
```

- 哈希 = `sha256(规范字节)`，规范字节 = `json.Marshal({name, version, metadata, endpoints})`。
  端点顺序由 `server.sortEndpoints` 固定，map 键由 `encoding/json` 排序。
- **用 metadata 键而不是新的线上字段**：vectors 的记录签名本来就覆盖 metadata，签名因此自动绑定了哈希。
  etcd / memory 原样传服务级 metadata；**consul 与 mdns 原先只传节点 metadata**，服务级的整个丢掉，
  2026-10-23 补上（consul 加 `s-` 标签、mdns 的 TXT 加 `ServiceMetadata`），分组同样改用 `GroupKey`。
- 没有 `volts.desc` 的记录（旧二进制、没开描述服务的进程）原样放行 —— 新旧可以混跑，etcd 前缀不变。

### 2.2 描述在哪、谁来答

- 进程内一张表 `哈希 → 规范字节`（`registry/descriptor.go`），注册时写入，进程退出即消失。
  **没有回收、不落盘、不写 etcd。**
- RPC 服务器上一条框架保留的端点 `registry.DescribeEndpoint`：入参一组哈希，回对应的规范字节。
  一次可取多份（kylin 一次约 4MB，RPC 单帧上限 64MiB）。
- 只挂 RPC（TCP）侧，不挂对外 HTTP 端口：描述里有端点表、设置字段、模块钩子。vectors 的 RPC 是
  mTLS + 只绑环回。该路由放在 `IsService=false` 的组里，不会被当成业务端点再写进描述。
- **只有本进程在答描述时才剥离**（`registry.SetDescriptorServing`，TCP 服务器构造时置位）。
  只跑 HTTP 服务器的进程照旧写完整记录，否则别人取不到它的描述。

### 2.3 读

- 解析器的 `GetService` / `ListServices` 拿到记录后按哈希补全：
  先查本进程表，再查描述缓存，都没有就批量去取。
  返回的仍是完整的 `registry.Service`（多一个不进线上格式的 `Digest` 字段），所有调用方不改。
- **去哪取**：从声明了这个哈希的节点里挑 `protocol=TCP` 的，失败换下一个。
  怎么发 RPC 由 client 包在 `init` 里经 `registry.SetDescriptorSource` 注入（registry 不能依赖
  client，client 依赖 registry）。
- **校验**：`sha256(字节) == 哈希`，描述里的服务名 == 记录的服务名。节点记录的签名覆盖了哈希，
  所以从哪个节点取、经什么链路都不影响可信度。
- **取不到**：这条记录这一轮不出现在结果里（与「节点还没起来」同义），同一哈希 2 秒内不重取；
  下一次读 / 刷新自然补上。
- **watch**：事件里的记录同样补全。create / update 补不全就跳过这条事件（下一轮全量刷新补）；
  delete 事件不需要描述，带上 Digest 原样交出。
- **缓存**：按哈希存解码后的描述，30 分钟没被用到的清掉（换版本后旧哈希不会一直占着）。

### 2.4 只读规矩

补全出来的 `Metadata` / `Endpoints` 是各次调用**共用**的那一份；`CopyService` 也不再逐个复制
Endpoint，只复制服务与节点（节点必须复制：客户端会改 `node.Address`）。
调用方**不许改** Endpoints / Metadata —— 2026-10-23 全仓核对过，没有任何调用方改它们。

### 2.5 分组

同名同版本、描述不同的记录（两个进程端点集不同）要分开：`registry.GroupKey(s)` 有哈希用哈希、
没有才退回 `EndpointsKey`。etcd 的 `GetService` 与 `registryCacher.update` 都改用它。

## 3. 收益（按实测各步开销推算）

| | 现在 | 之后 |
|---|---|---|
| etcd 里的注册数据 | 8.57 MB | 约 0.1 MB |
| 同一份二进制重启的写入 | 8.5 MB | 约 0.1 MB |
| 其它进程每收一次重启 | 整份重传、重解码、索引反复重建 | 约 0.2MB 小事件，描述 0 次解码 |
| 发新版本 | 同上 | 每个所属进程一次批量 RPC，每份描述解一次 |

## 4. vectors 侧

- `core/auth/registry_sig.go`：**不改**。签名覆盖 metadata，`volts.desc` 在里面。
- `module/service_index.go`：参与的「服务名 + 描述哈希」集合没变就不重建（只刷新 BuiltAt，不加 Gen、
  不通知监听方）；按哈希缓存每个服务的解析结果；合并窗口改成静默 300ms、最长 2s。

## 5. 断点清单

- [x] volts `registry/descriptor.go`：规范字节 / 哈希 / 校验、本进程表、描述缓存、注入点
- [x] volts `registry/resolver.go`：剥离、补全、批量取、watch 包装；`Use()` 返回包装后的后端
- [x] volts `registry`：`Service.Digest`、`GroupKey`、`CopyService` 共用端点
- [x] volts `registry/etcd`：`GetService` 按 `GroupKey` 分组
- [x] volts `registry/registry_cacher.go`：`update` 按 `GroupKey`
- [x] volts `server`：TCP 服务器置位 `SetDescriptorServing`、挂描述端点
- [x] volts `client`：`init` 注入取描述的实现
- [x] volts `router/proxy_route_skip.go`：有 Digest 时指纹用它
- [x] vectors `module/service_index.go`：不变不重建、按哈希缓存解析、合并窗口
- [x] 测试（见各文件）；`docs/claude/registry-selector.md` 补一节
- [x] 真栈：三进程一起重编重启，etcd 数据量、各进程分配量复测，故意重启一次 kylin 看另外两个

## 6. 真栈结果（2026-10-23，volts fd2290f + vectors 25983380，三进程 14:20 上线）

| | 之前 | 之后 |
|---|---|---|
| etcd 注册数据 | 8.6 MB（308 条） | 155 KB（308 条，全部瘦身） |
| connect_components 安静时分配 | 59 MB/分钟 | 23.5 MB/分钟（11 分钟 259MB） |
| connect_components 跨一次 websvc 重启（2 分钟） | 一次重启数 GB | 44 MB |
| connect_components 跨一次 kylin 重启（7 分钟，同一份二进制） | 30 分钟两次重启 11.9GB，其中索引重建 8.1GB | 294 MB，其中索引 59MB、etcd 读 60MB，描述 0 次重取 |

- 经网关的前台页面（catp4 / catp4.parts）与直连 websvc 字节一致；后台跨进程 `call_kw/<模型>/read`
  （res.partner、sale.order、website.page、connect.monitor）都正常。
- 进程起来的头一两秒会记「没有能答描述的节点（TCP）」：同一进程的 http 记录先于 rpc 记录写进
  注册中心，那一瞬间别人只看得到 http 节点。几秒内自愈，不是故障。
- 部署后 kylin 头十分钟每分钟约 1GB 分配，是定时任务 `AutoUpdateModules` 给各租户升级模块，
  与本改动无关。
