# IP/localhost 双绑定修复 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修复服务器绑定地址问题，使 `localhost:PORT`、`127.0.0.1:PORT` 配置时同时可通过本机 IP 和 localhost 访问。

**Architecture:** 在 `internal/net/net.go` 的 `Listen()` 入口新增 `normalizeBindAddr()` 将回环地址归一化为 `:PORT`（绑定全接口）；同时修复 `server/config.go` 的 `Address()` option 使 `cfg.Address` 与 `Transport.Addrs` 保持同步。

**Tech Stack:** Go 1.24.2, `net` 标准库, `go test`

---

### Task 1：修复 `internal/net/net.go` — 绑定地址归一化

**Files:**
- Modify: `internal/net/net.go`

- [ ] **Step 1：写失败测试**

在 `internal/net/net.go` 同级新建测试文件 `internal/net/net_test.go`（若已存在则追加）：

```go
package net

import (
	"net"
	"testing"
)

func TestNormalizeBindAddr(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"localhost:8080", ":8080"},
		{"127.0.0.1:8080", ":8080"},
		{"::1:8080", ":8080"},        // IPv6 loopback with port
		{":8080", ":8080"},           // already all-interface, unchanged
		{"0.0.0.0:8080", "0.0.0.0:8080"}, // explicit all-interface, unchanged
		{"192.168.1.10:8080", "192.168.1.10:8080"}, // specific IP, unchanged
		{"example.com:8080", "example.com:8080"},   // hostname, unchanged
		{"invalid-no-port", "invalid-no-port"},      // unparseable, unchanged
	}
	for _, c := range cases {
		got := normalizeBindAddr(c.input)
		if got != c.want {
			t.Errorf("normalizeBindAddr(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}
```

- [ ] **Step 2：运行测试，确认失败**

```bash
cd /Users/shadow/SectionZero/MyProject/Go/src/volts-dev/volts
go test ./internal/net/ -run TestNormalizeBindAddr -v
```

期望输出：`undefined: normalizeBindAddr` 编译错误。

- [ ] **Step 3：实现 `normalizeBindAddr` 并在 `Listen()` 调用**

编辑 `internal/net/net.go`，在 `Listen` 函数之前插入以下函数，并在 `Listen` 函数体首行调用：

```go
// normalizeBindAddr 将纯回环监听地址转换为全接口监听地址。
// localhost:PORT / 127.0.0.1:PORT / ::1:PORT → :PORT
// 其他地址原样返回。
func normalizeBindAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return ":" + port
	}
	return addr
}
```

修改 `Listen` 函数，在第一行加入归一化调用：

```go
func Listen(addr string, fn func(string) (net.Listener, error)) (net.Listener, error) {
	addr = normalizeBindAddr(addr) // 将回环地址扩展为全接口绑定

	if strings.Count(addr, ":") == 1 && strings.Count(addr, "-") == 0 {
		return fn(addr)
	}
	// ... 原有代码不变
```

- [ ] **Step 4：运行测试，确认通过**

```bash
go test ./internal/net/ -run TestNormalizeBindAddr -v
```

期望输出：
```
--- PASS: TestNormalizeBindAddr (0.00s)
PASS
```

- [ ] **Step 5：运行全量测试确认无回归**

```bash
go test ./internal/... -v
```

期望：所有测试 PASS，无 FAIL。

- [ ] **Step 6：提交**

```bash
git add internal/net/net.go internal/net/net_test.go
git commit -m "fix: normalize loopback bind addr to all-interface in Listen()"
```

---

### Task 2：修复 `server/config.go` — `Address()` option 字段同步

**Files:**
- Modify: `server/config.go:265-268`

- [ ] **Step 1：写失败测试**

在 `server/server_test.go` 追加以下测试（确认 `cfg.Address` 被正确设置）：

```go
func TestAddressOptionSyncsBothFields(t *testing.T) {
	const addr = ":19999"
	srv := New(Address(addr))
	cfg := srv.Config()

	if cfg.Address != addr {
		t.Errorf("cfg.Address = %q, want %q", cfg.Address, addr)
	}
	if cfg.Transport.Config().Addrs != addr {
		t.Errorf("Transport.Addrs = %q, want %q", cfg.Transport.Config().Addrs, addr)
	}
}
```

- [ ] **Step 2：运行测试，确认失败**

```bash
go test ./server/ -run TestAddressOptionSyncsBothFields -v
```

期望输出：
```
--- FAIL: TestAddressOptionSyncsBothFields
    cfg.Address = "", want ":19999"
```

- [ ] **Step 3：修复 `Address()` option**

编辑 `server/config.go`，将 `Address()` 函数改为：

```go
// Address to bind to - host:port or :port
func Address(addr string) Option {
	return func(cfg *Config) {
		cfg.Address = addr
		cfg.Transport.Config().Init(transport.WithAddrs(addr))
	}
}
```

- [ ] **Step 4：运行测试，确认通过**

```bash
go test ./server/ -run TestAddressOptionSyncsBothFields -v
```

期望输出：
```
--- PASS: TestAddressOptionSyncsBothFields (0.00s)
PASS
```

- [ ] **Step 5：运行 server 全量测试确认无回归**

```bash
go test ./server/ -v
```

期望：所有测试 PASS。

- [ ] **Step 6：提交**

```bash
git add server/config.go server/server_test.go
git commit -m "fix: Address() option now syncs cfg.Address and Transport.Addrs"
```

---

### Task 3：集成验证

**Files:**
- Test: `transport/tcp_test.go`（参考现有 TCP 测试写法）

- [ ] **Step 1：运行完整测试套件**

```bash
go test ./... -race
```

期望：全部 PASS，无 race condition 报告。

- [ ] **Step 2：手动验证双绑定行为**

在项目根目录新建临时验证脚本（验证后删除）：

```go
// verify_bind/main.go
package main

import (
	"fmt"
	"net"
	"net/http"
	"time"
)

func main() {
	ln, _ := net.Listen("tcp", ":19998") // 全接口绑定
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})}
	go srv.Serve(ln)
	time.Sleep(200 * time.Millisecond)

	// 通过 localhost 访问
	resp1, err1 := http.Get("http://localhost:19998/")
	fmt.Printf("localhost: %v err=%v\n", resp1.Status, err1)

	// 通过本机 IP 访问
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
				url := fmt.Sprintf("http://%s:19998/", ipnet.IP.String())
				resp2, err2 := http.Get(url)
				if err2 == nil {
					fmt.Printf("IP %s: %v\n", ipnet.IP, resp2.Status)
				}
			}
		}
	}
	srv.Close()
}
```

运行：
```bash
go run verify_bind/main.go
```

期望输出（IP 根据实际网卡而定）：
```
localhost: 200 OK err=<nil>
IP 192.168.x.x: 200 OK
```

- [ ] **Step 3：删除临时验证脚本并提交**

```bash
rm -rf verify_bind/
git add -A
git commit -m "fix: verified IP and localhost dual-bind works correctly"
```
