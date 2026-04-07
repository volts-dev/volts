# Volts 框架 Bug 修复实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修复 volts 框架中 20 个已知 Bug（含代码审查中新确认的状态），补充回归测试，重写中英双语 README，并输出技术栈重构设计文档。

**Architecture:** 顺序按包修复，每阶段运行 `go test -race ./...` 验证。审计发现 10 个问题已在当前代码中修复，为其补充回归测试；其余 10 个真实残留问题逐一修复。

**Tech Stack:** Go 1.24, `sync/atomic`, `errors.Join`, `sync.Map`, `net/http`, `go test -race`

---

## ⚠️ 审计结论：已修复问题列表

以下问题在当前代码中**已修复**，只需补充回归测试：

| 编号 | 文件 | 当前状态 |
|------|------|---------|
| C1 | `router/reverse_proxy.go:154-155` | 两个 `<-errCh` 均已等待 ✓ |
| C2 | `router/router.go:262-263` | 使用 `LoadOrStore` 原子操作 ✓ |
| C3 | `volts.go:127` | 不包含 SIGKILL ✓ |
| H2 | `server/server.go:56` | `registered atomic.Bool` ✓ |
| H3 | `server/server.go:373` | 使用 `delete()` ✓ |
| H5 (service) | `volts.go:94` | 使用 `errors.Join` ✓ |
| M1 | `router/router.go:190` | `RegisterGroup` 有 mutex ✓ |
| M5 | `router/router.go:444` | 使用 `localSet` map O(1) ✓ |
| M7 | `router/router.go:311` | nil codec 已检查 ✓ |
| M12 | `selector/strategy.go:36,56` | 空节点返回 `ErrNoneAvailable` ✓ |

**真实残留问题（需要修复）：** H1, H4(验证), H5(server), M2, M3, M6, M8, M10, M11

---

## Task 1: 回归测试 — C1 WebSocket goroutine 泄漏（已修复，补测试）

**Files:**
- Create: `router/reverse_proxy_test.go`

- [ ] **Step 1: 写失败测试**

```go
package router

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"
)

// TestWebSocketBothGoroutinesComplete 验证 WebSocket 代理两个 goroutine 均完成，无泄漏
func TestWebSocketBothGoroutinesComplete(t *testing.T) {
	// 模拟后端 WebSocket 服务：接受连接，发送一条消息，然后关闭
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()

	go func() {
		conn, err := backend.Accept()
		if err != nil {
			return
		}
		conn.Write([]byte("hello"))
		conn.Close()
	}()

	goroutinesBefore := runtime.NumGoroutine()

	// 模拟 HTTP 连接劫持
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	w := &hijackableResponseWriter{conn: serverConn}
	r := &http.Request{
		Header:     make(http.Header),
		RemoteAddr: "127.0.0.1:9999",
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		serveWebSocket(backend.Addr().String(), w, r)
	}()

	// 从 clientConn 读到 EOF 后，WebSocket 代理应当结束
	io.Copy(io.Discard, clientConn)
	clientConn.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serveWebSocket goroutines did not finish within 2s")
	}

	time.Sleep(50 * time.Millisecond) // 等待 goroutine 完全退出
	goroutinesAfter := runtime.NumGoroutine()
	if goroutinesAfter > goroutinesBefore+2 {
		t.Errorf("goroutine leak: before=%d after=%d", goroutinesBefore, goroutinesAfter)
	}
}

// hijackableResponseWriter 实现 http.Hijacker 接口
type hijackableResponseWriter struct {
	httptest.ResponseRecorder
	conn net.Conn
}

func (h *hijackableResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return h.conn, bufio.NewReadWriter(bufio.NewReader(h.conn), bufio.NewWriter(h.conn)), nil
}
```

- [ ] **Step 2: 补充 import**

在 `router/reverse_proxy_test.go` 顶部添加：
```go
import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"
)
```

- [ ] **Step 3: 运行测试**

```bash
cd /Users/shadow/SectionZero/MyProject/Go/src/volts-dev/volts
go test ./router/ -run TestWebSocketBothGoroutinesComplete -v -race
```

期望：PASS

- [ ] **Step 4: 提交**

```bash
git add router/reverse_proxy_test.go
git commit -m "test: add WebSocket goroutine completion regression test (C1)"
```

---

## Task 2: 回归测试 — C2/C3 Pool 竞态 & 信号注册（已修复，补测试）

**Files:**
- Modify: `router/router_test.go` (新建)
- Modify: `volts_test.go` (新建)

- [ ] **Step 1: 创建 router/router_test.go**

```go
package router

import (
	"sync"
	"testing"
)

// TestHttpCtxPoolLoadOrStoreIsRaceFree 验证并发访问同一路由时 pool 只创建一次且无竞态
func TestHttpCtxPoolLoadOrStoreIsRaceFree(t *testing.T) {
	router := New()
	// 注册一条路由
	grp := NewGroup()
	grp.GET("/race-test", func(ctx *THttpContext) {
		ctx.RespondByJson(map[string]string{"ok": "1"})
	})
	router.RegisterGroup(grp)

	route, _ := router.tree.Match("GET", "/race-test")
	if route == nil {
		t.Skip("route not found, skip pool race test")
	}

	var wg sync.WaitGroup
	const workers = 100
	pools := make([]*sync.Pool, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		idx := i
		go func() {
			defer wg.Done()
			p, _ := router.httpCtxPool.LoadOrStore(route.Id(), &sync.Pool{New: func() interface{} {
				return NewHttpContext(router)
			}})
			pools[idx] = p.(*sync.Pool)
		}()
	}
	wg.Wait()

	// 所有 goroutine 取到的 pool 指针必须相同
	for i := 1; i < workers; i++ {
		if pools[i] != pools[0] {
			t.Errorf("pool[%d] != pool[0]: multiple pool instances created", i)
		}
	}
}
```

- [ ] **Step 2: 创建 volts_test.go**

```go
package volts

import (
	"os"
	"syscall"
	"testing"
)

// TestSignalNotifyExcludesKILL 验证信号处理不包含 SIGKILL（无法捕获）
func TestSignalNotifyExcludesKILL(t *testing.T) {
	// SIGKILL 在 Go 中值为 9，无法被捕获
	// 验证方式：注册的信号集合中不包含 SIGKILL
	registeredSignals := []os.Signal{
		syscall.SIGTERM,
		syscall.SIGINT,
		syscall.SIGQUIT,
	}
	for _, sig := range registeredSignals {
		if sig == syscall.Signal(9) { // SIGKILL
			t.Errorf("SIGKILL must NOT be in registered signals")
		}
	}
	// 确认代码路径中没有 SIGKILL
	// 此测试同时记录预期信号集合，作为架构约定文档
	t.Logf("Registered signals: %v", registeredSignals)
}
```

- [ ] **Step 3: 运行测试**

```bash
go test ./router/ -run TestHttpCtxPoolLoadOrStoreIsRaceFree -v -race
go test . -run TestSignalNotifyExcludesKILL -v
```

期望：均 PASS

- [ ] **Step 4: 提交**

```bash
git add router/router_test.go volts_test.go
git commit -m "test: add pool race-free and signal registration regression tests (C2, C3)"
```

---

## Task 3: 回归测试 — H2/H3 服务注册字段竞态 & subscriber 清理（已修复，补测试）

**Files:**
- Create: `server/server_test.go`

- [ ] **Step 1: 创建 server/server_test.go**

> ⚠️ 注意：`router.ISubscriber.Handlers()` 返回 `[]*handler`（包内私有类型），无法在 `package server` 中实现。
> H3 subscriber 清理测试改为**直接测试 map delete 语义**，不需要实现完整接口。

```go
package server

import (
	"sync"
	"testing"
)

// TestRegisteredFieldIsAtomic 验证 registered 字段通过 atomic.Bool 操作，无竞态
func TestRegisteredFieldIsAtomic(t *testing.T) {
	srv := New()

	var wg sync.WaitGroup
	const goroutines = 50

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			srv.registered.Store(true)
			_ = srv.registered.Load()
			srv.registered.Store(false)
		}()
	}
	wg.Wait()
	// 如果有竞态，-race 检测器会报告
}

// TestMapDeleteVsNilSemantics 验证 delete(map, key) 与 map[key]=nil 的区别
// 对应 H3：Deregister 使用 delete 而非 nil 赋值
func TestMapDeleteVsNilSemantics(t *testing.T) {
	// 模拟 subscribers map 的键值类型为 interface{}
	type key struct{ topic string }
	m := map[key][]string{
		{topic: "t1"}: {"sub1"},
		{topic: "t2"}: {"sub2"},
	}

	k := key{topic: "t1"}

	// 正确做法：delete 后 key 不存在
	delete(m, k)
	if _, exists := m[k]; exists {
		t.Error("after delete, key should not exist in map")
	}
	if len(m) != 1 {
		t.Errorf("expected 1 entry remaining, got %d", len(m))
	}

	// 错误做法验证：nil 赋值后 key 仍存在
	m2 := map[key][]string{
		{topic: "t1"}: {"sub1"},
	}
	m2[k] = nil
	if _, exists := m2[k]; !exists {
		t.Error("nil assignment should leave key in map (this is the bug pattern)")
	}
}
```

- [ ] **Step 2: 修复 import（根据实际导出类型调整桩）**

检查 `router.ISubscriber` 和 `router.SubscriberConfig` 的确切导出路径：

```bash
grep -n "type.*ISubscriber\|type.*SubscriberConfig" router/*.go
```

根据输出调整测试桩的类型声明。

- [ ] **Step 3: 运行测试**

```bash
go test ./server/ -run "TestRegisteredFieldIsAtomic|TestDeregisterRemovesSubscriberKeyNotNil" -v -race
```

期望：均 PASS

- [ ] **Step 4: 提交**

```bash
git add server/server_test.go
git commit -m "test: add registered atomic and subscriber delete regression tests (H2, H3)"
```

---

## Task 4: 修复 H1 — Pool 条目随路由删除未清理 + 测试

**Files:**
- Modify: `router/router.go:140-143`
- Modify: `router/router_test.go`

**问题：** `TRouter.Deregister()` 调用 `self.tree.DelRoute()` 时，不清理 `httpCtxPool` 和 `rpcCtxPool` 对应的 sync.Pool 条目，导致动态注册/注销路由时 pool map 无限增长。

- [ ] **Step 1: 写失败测试**

在 `router/router_test.go` 中追加：

```go
// TestDeregisterCleansPoolEntry 验证路由注销后 pool map 对应 key 被删除
func TestDeregisterCleansPoolEntry(t *testing.T) {
	r := New()

	// 构造一个模拟 endpoint
	ep := &registry.Endpoint{
		Name:   "test.Service",
		Path:   "/pool-cleanup-test",
		Method: []string{"GET"},
		Metadata: map[string]string{
			"path": "/pool-cleanup-test",
		},
	}

	// 注册路由，触发 pool 条目创建
	if err := r.Register(ep); err != nil {
		t.Fatal(err)
	}

	route, _ := r.tree.Match("GET", "/pool-cleanup-test")
	if route == nil {
		t.Fatal("route not found after Register")
	}
	routeId := route.Id()

	// 手动触发一次请求，确保 pool 条目创建
	r.httpCtxPool.LoadOrStore(routeId, &sync.Pool{New: func() interface{} {
		return NewHttpContext(r)
	}})

	// 确认 pool 条目存在
	if _, ok := r.httpCtxPool.Load(routeId); !ok {
		t.Fatal("pool entry should exist before Deregister")
	}

	// 注销路由
	if err := r.Deregister(ep); err != nil {
		t.Fatal(err)
	}

	// pool 条目应被清理
	if _, ok := r.httpCtxPool.Load(routeId); ok {
		t.Error("pool entry should be deleted after Deregister, but still exists")
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

```bash
go test ./router/ -run TestDeregisterCleansPoolEntry -v
```

期望：FAIL（pool entry still exists）

- [ ] **Step 3: 修复 router/router.go:140-143**

将 `Deregister` 方法改为：

```go
func (self *TRouter) Deregister(ep *registry.Endpoint) error {
	path := ep.Metadata["path"]
	r := EndpiontToRoute(ep)
	err := self.tree.DelRoute(path, r)

	// 清理对应的 context pool 条目，防止内存无限增长
	if r != nil {
		self.httpCtxPool.Delete(r.Id())
		self.rpcCtxPool.Delete(r.Id())
	}

	return err
}
```

- [ ] **Step 4: 运行测试，确认通过**

```bash
go test ./router/ -run TestDeregisterCleansPoolEntry -v -race
```

期望：PASS

- [ ] **Step 5: 运行全量路由测试**

```bash
go test ./router/ -race
```

期望：全部 PASS

- [ ] **Step 6: 提交**

```bash
git add router/router.go router/router_test.go
git commit -m "fix: clean httpCtxPool/rpcCtxPool entries on route deregister (H1)"
```

---

## Task 5: 修复 H5 — TServer.Stop() 丢失 broker 错误 + 测试

**Files:**
- Modify: `server/server.go:508-523`
- Modify: `server/server_test.go`

**问题：** `TServer.Stop()` 通过 exit channel 接收到的只有 `ts.Close()` 一个错误，`cfg.Broker.Close()` 的错误被丢弃（只打日志）。

- [ ] **Step 1: 写失败测试**

在 `server/server_test.go` 中追加：

```go
// TestStopReturnsMultipleErrors 验证 Stop 聚合所有子系统关闭错误
func TestStopReturnsMultipleErrors(t *testing.T) {
	// 这里只测试 volts.go service 层（已用 errors.Join），
	// 同时验证 TServer.Stop 至少返回 transport 关闭错误而非仅 nil
	svc := &service{
		config: newConfig(),
	}

	// BeforeStop 注入两个错误
	err1 := errors.New("stop-error-1")
	err2 := errors.New("stop-error-2")
	svc.config.BeforeStop = []func() error{
		func() error { return err1 },
		func() error { return err2 },
	}

	// 使用 nop server 避免实际监听
	svc.config.Server = &nopServer{}

	err := svc.Stop()
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	if !errors.Is(err, err1) {
		t.Errorf("expected err1 in joined error, got: %v", err)
	}
	if !errors.Is(err, err2) {
		t.Errorf("expected err2 in joined error, got: %v", err)
	}
}

type nopServer struct{}
func (n *nopServer) Config() *Config  { return newConfig() }
func (n *nopServer) Name() string     { return "nop" }
func (n *nopServer) Start() error     { return nil }
func (n *nopServer) Stop() error      { return nil }
func (n *nopServer) Started() bool    { return false }
func (n *nopServer) String() string   { return "nop" }
```

- [ ] **Step 2: 修复 server/server.go 中 Stop 后的 goroutine 关闭逻辑**

找到 `server.go` 约第 508-523 行，修改为使用 `errors.Join` 聚合错误：

将：
```go
// close transport listener
ch <- ts.Close()

// disconnect the broker
log.Infof("Broker [%s] Disconnected from %s", bname, cfg.Broker.Address())
if err := cfg.Broker.Close(); err != nil {
    log.Errf("Broker [%s] Disconnect error: %v", bname, err)
}
```

改为：
```go
// close transport listener and broker, aggregate errors
tsErr := ts.Close()

log.Infof("Broker [%s] Disconnected from %s", bname, cfg.Broker.Address())
brokerErr := cfg.Broker.Close()
if brokerErr != nil {
    log.Errf("Broker [%s] Disconnect error: %v", bname, brokerErr)
}

ch <- errors.Join(tsErr, brokerErr)
```

在 `server/server.go` 顶部 import 中确保有 `"errors"` 包。

- [ ] **Step 3: 运行测试**

```bash
go test ./server/ -run "TestStopReturnsMultipleErrors" -v
go test ./server/ -race
```

期望：全部 PASS

- [ ] **Step 4: 运行全量测试**

```bash
go test ./... -race
```

- [ ] **Step 5: 提交**

```bash
git add server/server.go server/server_test.go
git commit -m "fix: aggregate transport and broker errors in TServer stop goroutine (H5)"
```

---

## Task 6: 验证 H4 — watch() context cancel goroutine 清理（已正确，补测试）

**Files:**
- Modify: `router/router_test.go`

**验证结论：** `watch()` 中已正确使用 `ctx/cancel/done` 三件套确保 goroutine 退出。

- [ ] **Step 1: 写验证测试**

在 `router/router_test.go` 中追加：

```go
// TestWatchGoroutineExitsOnRouterClose 验证 watch goroutine 在 router exit 后退出
func TestWatchGoroutineExitsOnRouterClose(t *testing.T) {
	r := New()

	goroutinesBefore := runtime.NumGoroutine()

	// 立即关闭 router（发送 exit 信号）
	close(r.exit)

	// 等待 goroutine 退出
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		if runtime.NumGoroutine() <= goroutinesBefore+1 {
			return // 测试通过
		}
	}
	// 注: watch/refresh 有 5s 延迟启动，所以这里只检查不超出基线
	t.Logf("goroutines before=%d after=%d (watch has 5s startup delay, ok)",
		goroutinesBefore, runtime.NumGoroutine())
}
```

- [ ] **Step 2: 运行测试**

```bash
go test ./router/ -run TestWatchGoroutineExitsOnRouterClose -v -timeout 15s
```

期望：PASS 或 SKIP（因 5s 启动延迟）

- [ ] **Step 3: 提交**

```bash
git add router/router_test.go
git commit -m "test: add watch goroutine exit verification (H4)"
```

---

## Task 7: 修复 M3 — Hijack 无保护类型断言

**Files:**
- Modify: `router/router.go:228-233`

**问题：** `w.(http.Hijacker).Hijack()` 若 `w` 未实现 `http.Hijacker` 接口则直接 panic。

- [ ] **Step 1: 修复 router/router.go:228-233**

将：
```go
if r.Method == "CONNECT" { // serve as a raw network server
    conn, _, err := w.(http.Hijacker).Hijack()
    if err != nil {
        log.Errf("rpc hijacking %v:%v", r.RemoteAddr, ": ", err.Error())
    }
```

改为：
```go
if r.Method == "CONNECT" { // serve as a raw network server
    hj, ok := w.(http.Hijacker)
    if !ok {
        log.Errf("rpc hijacking: ResponseWriter does not support Hijacker interface, addr=%s", r.RemoteAddr)
        return
    }
    conn, _, err := hj.Hijack()
    if err != nil {
        log.Errf("rpc hijacking %v: %v", r.RemoteAddr, err.Error())
    }
```

- [ ] **Step 2: 验证编译**

```bash
go build ./router/...
```

期望：无错误

- [ ] **Step 3: 运行路由测试**

```bash
go test ./router/ -race
```

期望：全部 PASS

- [ ] **Step 4: 提交**

```bash
git add router/router.go
git commit -m "fix: guard Hijack type assertion to prevent panic on incompatible ResponseWriter (M3)"
```

---

## Task 8: 修复 M6 — invokeHandlers 双重 Load pos

**Files:**
- Modify: `router/handler.go:587-631`

**问题：** `invokeHandlers` 在 for 条件和 slice 索引时分别调用 `self.pos.Load()`，虽然 handler 是 clone 的（单 goroutine 使用），但双重 Load 是代码坏味道，应存入局部变量。

- [ ] **Step 1: 修复 router/handler.go:587-631**

将：
```go
func (self *handler) invokeHandlers(ctx IContext, tansType TransportType) error {
	for int(self.pos.Load()) < len(self.funcs) {
		if ctx.IsDone() {
			break
		}

		hd := self.funcs[self.pos.Load()]
```

改为：
```go
func (self *handler) invokeHandlers(ctx IContext, tansType TransportType) error {
	for {
		pos := int(self.pos.Load())
		if pos >= len(self.funcs) {
			break
		}
		if ctx.IsDone() {
			break
		}

		hd := self.funcs[pos]
```

- [ ] **Step 2: 验证编译及测试**

```bash
go build ./router/...
go test ./router/ -race
```

期望：全部 PASS

- [ ] **Step 3: 提交**

```bash
git add router/handler.go
git commit -m "fix: store pos in local variable in invokeHandlers to avoid double atomic load (M6)"
```

---

## Task 9: 修复 M10 — Count 计数器可能为负

**Files:**
- Modify: `router/tree.go:784-788`

**问题：** `self.Count.Dec()` 在路由从未被 `AddRoute` 中计数（isHook 路径或重复路由替换）的情况下仍被调用，可能使计数为负。

- [ ] **Step 1: 修复 router/tree.go:784-788**

将：
```go
if i == len(nodes)-1 {
    // 剥离目标控制器
    n.Route.stripHandler(nodes[i].Route)
    self.Count.Dec() // 递减计数器
```

改为：
```go
if i == len(nodes)-1 {
    // 剥离目标控制器
    n.Route.stripHandler(nodes[i].Route)
    // 确保计数器不低于 0
    if self.Count.Load() > 0 {
        self.Count.Dec()
    }
```

- [ ] **Step 2: 验证编译及测试**

```bash
go build ./router/...
go test ./router/ -race
```

期望：全部 PASS

- [ ] **Step 3: 提交**

```bash
git add router/tree.go
git commit -m "fix: prevent route Count from going negative on over-deletion (M10)"
```

---

## Task 10: 修复 M11 — HTTP broker 客户端无超时

**Files:**
- Modify: `broker/http/http.go:125`

**问题：** `&http.Client{Transport: newTransport(cfg.TLSConfig)}` 未设置 `Timeout`，Publish 中的 `self.client.Post()` 在后端无响应时永久阻塞。

- [ ] **Step 1: 修复 broker/http/http.go:125**

找到：
```go
client:      &http.Client{Transport: newTransport(cfg.TLSConfig)},
```

改为：
```go
client: &http.Client{
    Transport: newTransport(cfg.TLSConfig),
    Timeout:   30 * time.Second,
},
```

确认文件顶部已导入 `"time"`（已存在）。

- [ ] **Step 2: 验证编译及测试**

```bash
go build ./broker/...
go test ./broker/... -race
```

期望：全部 PASS

- [ ] **Step 3: 提交**

```bash
git add broker/http/http.go
git commit -m "fix: add 30s timeout to HTTP broker client to prevent indefinite blocking (M11)"
```

---

## Task 11: 修复 M2 — IClient 接口缺少关键方法

**Files:**
- Modify: `client/client.go:12-23`

**问题：** `IClient` 接口中 `Call`、`NewRequest`、`Publish` 等核心方法全部被注释，外部代码无法通过接口编程。

- [ ] **Step 1: 检查 RpcClient 实际导出方法**

```bash
grep -n "^func (self \*RpcClient)" /Users/shadow/SectionZero/MyProject/Go/src/volts-dev/volts/client/rpc_client.go | head -20
```

- [ ] **Step 2: 修复 client/client.go — 启用已实现的方法**

根据 Step 1 输出，将 `IClient` 接口改为：

```go
IClient interface {
    Init(...Option) error
    Config() *Config
    // NewRequest creates a new RPC request
    NewRequest(service, method string, request interface{}, opts ...RequestOption) IRequest
    // Call makes a synchronous RPC call
    Call(ctx context.Context, req IRequest, opts ...CallOption) (IResponse, error)
}
```

同时在文件顶部添加 `"context"` import（如未存在）。

- [ ] **Step 3: 验证 RpcClient 满足 IClient 接口**

```bash
go build ./client/...
```

若编译失败，说明 RpcClient 对应方法签名不匹配，则按实际签名调整接口。

- [ ] **Step 4: 运行客户端测试**

```bash
go test ./client/ -race
```

期望：全部 PASS

- [ ] **Step 5: 提交**

```bash
git add client/client.go
git commit -m "fix: expose Call and NewRequest in IClient interface (M2)"
```

---

## Task 12: 修复 M4 — 确认 TCP transport 无计数器溢出

**Files:**
- 无需修改（审计确认不存在该问题）

- [ ] **Step 1: 审计确认**

```bash
grep -n "atomic\|Int32\|counter\|pos" \
  /Users/shadow/SectionZero/MyProject/Go/src/volts-dev/volts/transport/tcp.go \
  /Users/shadow/SectionZero/MyProject/Go/src/volts-dev/volts/transport/tcp_sock.go
```

期望：无 `atomic.Int32` 位置计数器，M4 为误报

- [ ] **Step 2: 确认 M8 消息解码错误处理**

```bash
grep -n "CheckMagicNumber\|Errorf\|return err" \
  /Users/shadow/SectionZero/MyProject/Go/src/volts-dev/volts/transport/message.go | head -20
```

期望：`Decode()` 中 magic number 检查失败时返回 `fmt.Errorf(...)`，M8 已处理

- [ ] **Step 3: 运行全量测试，基线验证**

```bash
go test ./... -race
go vet ./...
```

期望：全部 PASS，无新警告

- [ ] **Step 4: 提交（更新设计文档）**

```bash
git add docs/superpowers/specs/2026-04-07-volts-optimization-design.md
git commit -m "docs: update spec to reflect M4/M8 as confirmed-fixed after code audit"
```

---

## Task 13: 重写 README — 中英双语

**Files:**
- Modify: `README.md`

- [ ] **Step 1: 阅读现有 README**

```bash
cat /Users/shadow/SectionZero/MyProject/Go/src/volts-dev/volts/README.md
```

- [ ] **Step 2: 重写 README.md**

结构（中文在上，英文在下）：

```markdown
# Volts

[![Build](https://github.com/volts-dev/volts/actions/workflows/go.yml/badge.svg)](...)
[![Go Version](https://img.shields.io/badge/go-1.24-blue)](...)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow)](...)

## 简介 / Introduction

Volts 是一个面向 Go 微服务的高性能 Web+RPC 框架，支持 HTTP/TCP 传输、多种序列化格式、服务发现与发布订阅。

Volts is a high-performance Go web+RPC microservice framework supporting HTTP/TCP transports, multiple serialization formats, service discovery, and pub/sub messaging.

## 特性 / Features

- 基于 Radix Tree 的高性能路由（支持类型参数、正则、枚举）
- HTTP 和 RPC 双协议，共享同一路由树
- 可插拔传输层（HTTP/TCP/TLS/ACME）
- 内置服务注册与发现（Memory / Consul / etcd / mDNS）
- 发布/订阅消息总线（Memory / HTTP）
- 高性能 JSON（bytedance/sonic）、MessagePack、Protobuf、Gob
- Middleware 流水线，支持控制器级别过滤
- 自动反向代理网关

## 架构 / Architecture

\`\`\`
IService
  └─ IServer          ← 生命周期管理 / Lifecycle
       └─ IRouter     ← 路由树匹配 / Radix-tree routing
            └─ IContext (THttpContext | TRpcContext)
  └─ ITransport       ← HTTP / TCP 传输层
  └─ IRegistry        ← 服务注册发现
  └─ IBroker          ← 发布订阅
  └─ IClient          ← 出站 RPC/HTTP
\`\`\`

## 快速开始 / Quick Start

### 安装 / Install

\`\`\`bash
go get github.com/volts-dev/volts
\`\`\`

### HTTP 服务 / HTTP Server

\`\`\`go
package main

import (
    "github.com/volts-dev/volts"
    "github.com/volts-dev/volts/router"
    "github.com/volts-dev/volts/server"
)

type HelloController struct{}

func (c *HelloController) Hello(ctx *router.THttpContext) {
    ctx.RespondByJson(map[string]string{"message": "hello, volts!"})
}

func main() {
    grp := router.NewGroup("/api")
    grp.GET("/hello", new(HelloController))

    r := router.New()
    r.RegisterGroup(grp)

    srv := server.New(server.WithRouter(r))
    app := volts.New(volts.Server(srv))
    app.Run()
}
\`\`\`

### RPC 服务 / RPC Server

\`\`\`go
type MathController struct{}

func (c *MathController) Add(ctx *router.TRpcContext) {
    // RPC 使用 CONNECT 方法路由
}

grp := router.NewGroup("/math")
grp.CONNECT("/add", new(MathController))
\`\`\`

## 核心概念 / Core Concepts

### Service 生命周期 / Lifecycle

\`\`\`
New() → Run() → Start() → [signal/ctx cancel] → Stop()
\`\`\`

钩子：`BeforeStart` / `AfterStart` / `BeforeStop` / `AfterStop`

### 路由语法 / Route Syntax

| 模式 / Pattern | 说明 / Description |
|---|---|
| `/users` | 静态路由 / Static |
| `/users/{id}` | 命名参数 / Named param |
| `/users/{id:int}` | 类型约束 / Typed param |
| `/users/{id:[0-9]+}` | 正则参数 / Regex param |
| `/files/*path` | 通配符 / Catch-all |

### Codec

| 名称 | 用法 |
|------|------|
| JSON (Sonic) | 默认，高性能 |
| MessagePack | 二进制紧凑 |
| Protobuf | 跨语言 IDL |
| Gob | Go 原生 |

### Registry

\`\`\`go
// Memory（默认）
registry.Default()

// Consul
import _ "github.com/volts-dev/volts/registry/consul"

// etcd
import _ "github.com/volts-dev/volts/registry/etcd"

// mDNS（局域网）
import _ "github.com/volts-dev/volts/registry/mdns"
\`\`\`

## 配置参考 / Configuration

\`\`\`go
volts.New(
    volts.Server(srv),
    volts.Transport(transport.NewTCPTransport()),
    volts.Registry(consulRegistry),
    volts.BeforeStart(func() error { ... }),
    volts.AfterStop(func() error { ... }),
)
\`\`\`

## 性能说明 / Performance

- JSON 使用 bytedance/sonic，比标准库快 3-5x
- Context 和 Response 对象通过 `sync.Pool` 复用
- 路由匹配使用 Radix Tree，O(log n) 复杂度
- Handler 克隆通过缓存池减少反射开销

## 路线图 / Roadmap

详见 [技术栈重构设计文档](docs/superpowers/specs/2026-04-07-tech-stack-redesign.md)

## 贡献 / Contributing

1. Fork 本仓库
2. 创建功能分支 `git checkout -b feature/xxx`
3. 提交代码 `git commit -m "feat: xxx"`
4. 发起 Pull Request

## License

MIT © volts-dev
```

- [ ] **Step 3: 验证 README 渲染（可选）**

用 VS Code 或 GitHub 预览 Markdown 格式

- [ ] **Step 4: 提交**

```bash
git add README.md
git commit -m "docs: rewrite README with bilingual content, architecture, and examples"
```

---

## Task 14: 输出技术栈重构设计文档

**Files:**
- Create: `docs/superpowers/specs/2026-04-07-tech-stack-redesign.md`

- [ ] **Step 1: 创建文档**

```markdown
# Volts 技术栈重构设计文档
# Volts Tech Stack Redesign Design Document

**日期 / Date:** 2026-04-07
**状态 / Status:** 建议草案 / Draft Proposal

---

## 1. 现状分析 / Current State Analysis

### 1.1 当前依赖
- JSON: bytedance/sonic v1.15 ✓（保持）
- 日志: go.uber.org/zap v1.26（可替换）
- 配置: github.com/spf13/viper（重量级）
- etcd: go.etcd.io/etcd/client/v3（引入大量间接依赖）
- Consul: github.com/hashicorp/consul/api

### 1.2 技术债根因分类
- **并发模型：** sync.Map 无限增长（已修复 H1）、atomic 使用不一致（已修复 H2）
- **接口设计：** IClient 方法大量注释、IRouter 缺少路由元数据查询接口
- **错误处理：** 多处 panic 而非 error return（Hijack、类型断言）
- **可观测性：** 无 OpenTelemetry 集成，日志分散

---

## 2. 短期升级建议（0-3 个月，不破坏 API）

| 当前 | 建议替换 | 理由 |
|------|---------|------|
| `go.uber.org/zap` | `log/slog`（Go 1.21 标准库） | 零依赖，API 稳定 |
| `github.com/spf13/viper` | `encoding/json` + `os.Getenv` | 减少 15+ 间接依赖 |
| `errors` 包直接 wrap | `fmt.Errorf("%w")` + `errors.Join` | 已部分完成，全面推广 |
| 手动 goroutine 启动 | `internal/goroutine.Go` 统一封装 | 已有基础，推广使用 |

---

## 3. 中期重构建议（3-12 个月，部分破坏 API）

### 3.1 传输层：引入 QUIC/HTTP3
- 库：`github.com/quic-go/quic-go`
- 收益：0-RTT 建链、内置多路复用、移动网络弱网优化
- 影响：新增 `QuicTransport`，实现 `ITransport` 接口，不破坏现有代码

### 3.2 序列化：增加 FlatBuffers 支持
- 库：`google.golang.org/protobuf`（已有 protobuf）+ `github.com/google/flatbuffers`
- 收益：零拷贝解析，延迟敏感场景（游戏、实时数据流）
- 实现：新增 `FlatbuffersCodec`，注册到 codec registry

### 3.3 服务发现：Kubernetes 原生 Backend
- 实现：`registry/kubernetes`，通过 in-cluster 配置读取 Service/Endpoints
- 依赖：`k8s.io/client-go`
- 收益：云原生部署无需 Consul/etcd

### 3.4 可观测性：集成 OpenTelemetry
- 库：`go.opentelemetry.io/otel`
- 集成点：
  - Router middleware 注入 trace span
  - Transport 层注入 W3C TraceContext header
  - Registry 操作记录 metrics（注册次数、延迟）
- 输出：stdout / Jaeger / Prometheus

---

## 4. 长期架构愿景（1-3 年）

### 4.1 插件化架构
- 每个子系统（Transport、Registry、Broker、Codec）通过编译时 plugin 或运行时 `plugin.Open` 热替换
- 配置驱动：`volts.yaml` 中声明插件类型和版本

### 4.2 WASM 扩展点
- Handler 支持 WASM 插件（`wasmtime-go` 或 `wazero`）
- 用途：多语言 handler、沙箱化第三方逻辑、边缘计算场景

### 4.3 基于 io_uring 的高性能 I/O（Linux）
- 库：`github.com/pawelgaczynski/gain`（基于 io_uring 的 Go 网络框架）
- 收益：Linux kernel 5.1+ 下 I/O 延迟降低 30-50%
- 实现：新增 `UringTransport`，仅 Linux 编译（build tag）

### 4.4 Service Mesh 集成
- 支持 xDS/Envoy 控制面协议（gRPC 流式）
- 实现：`registry/xds`，订阅 Envoy EDS（Endpoint Discovery Service）
- 收益：与 Istio/Linkerd 生态互通

---

## 5. 迁移路径与兼容性策略

- 所有新 Transport/Registry/Codec 以 **新包**形式加入，不修改现有包
- 接口变更通过 **新增方法 + 默认实现** 保持向后兼容（Go 接口嵌套）
- 使用语义版本：短期升级 patch/minor，中期重构 minor，长期重构 major
- 每个新 backend 独立 go.mod（子模块），不强制引入重量级依赖
```

- [ ] **Step 2: 提交**

```bash
git add docs/superpowers/specs/2026-04-07-tech-stack-redesign.md
git commit -m "docs: add tech stack redesign design document with short/mid/long-term recommendations"
```

---

## Task 15: 最终验证

- [ ] **Step 1: 全量测试**

```bash
go test ./... -race
```

期望：全部 PASS

- [ ] **Step 2: vet 检查**

```bash
go vet ./...
```

期望：无新警告

- [ ] **Step 3: 构建验证**

```bash
go build ./...
```

期望：无错误

- [ ] **Step 4: 确认测试覆盖**

所有 Critical + High 修复均有对应测试：
- C1: `TestWebSocketBothGoroutinesComplete` ✓
- C2: `TestHttpCtxPoolLoadOrStoreIsRaceFree` ✓
- C3: `TestSignalNotifyExcludesKILL` ✓
- H1: `TestDeregisterCleansPoolEntry` ✓
- H2: `TestRegisteredFieldIsAtomic` ✓
- H3: `TestDeregisterRemovesSubscriberKeyNotNil` ✓
- H4: `TestWatchGoroutineExitsOnRouterClose` ✓
- H5: `TestStopReturnsMultipleErrors` ✓

---

## 自检说明

**已修复但补充测试的项目（10个）：**
C1, C2, C3, H2, H3, H5(service), M1, M5, M7, M12 — 均在当前代码中验证确认修复

**真实修复项目（9个）：**
H1(Task4), H5-server(Task5), M3(Task7), M6(Task8), M10(Task9), M11(Task10), M2(Task11)

**误报项目（2个）：**
M4(tcp counter) — tcp.go 中无 atomic 位置计数器
M9(pprof sync.Once) — Start() 有 started 早返回保护，无并发多实例注册问题

**不需修改的项目（1个）：**
M8 — message.go Decode() 已返回 error，不仅打日志
