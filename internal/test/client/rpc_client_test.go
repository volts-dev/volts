package client

import (
	"net"
	"testing"
	"time"

	"github.com/volts-dev/volts"
	"github.com/volts-dev/volts/client"
	"github.com/volts-dev/volts/internal/test"
	"github.com/volts-dev/volts/router"
	"github.com/volts-dev/volts/server"
	"github.com/volts-dev/volts/transport"
)

// TestHelloworld 起一个 TCP/RPC 服务端并用 RpcClient 打一次真实调用。
//
// NOTE 原实现有三个问题，任何一个都会让本包在 CI 上挂满整个 timeout（实测 307s）
// 并掩盖真实错误：
//  1. `go func(){ app.Run() }()` 丢弃返回值 —— Run 启动失败时直接静默退出，
//     而下面的 g.Wait() 会永远等一个再也不会到来的 Started()
//  2. 就绪探测用 `time.Sleep(500)`，那是 **500 纳秒**（不是 500ms），
//     等于忙等空转，还没有任何超时上限
//  3. 跑完不 Stop，服务端与全部后台协程留到进程结束
//  4. 用 volts.Debug() 起服务 —— Debug 模式会把端口**钉死**在 45999，
//     于是本测试与开发机上正在跑的服务（以及并行执行的其他测试包）必然抢端口，
//     报的却是完全无关的"服务端未就绪"。现改为申请空闲端口。
func TestHelloworld(t *testing.T) {
	r := router.New()
	r.Url([]string{"CONNECT"}, "Arith", new(test.ArithCtrl))

	addr := freeAddr(t)

	srv := server.New(
		server.WithRouter(r),
		server.Address(addr),
	)

	app := volts.New(
		volts.Server(srv),
		volts.Transport(transport.NewTCPTransport()),
	)

	// Run 的错误必须被看见 —— 它是"服务端没起来"的唯一线索
	runErr := make(chan error, 1)
	go func() { runErr <- app.Run() }()

	t.Cleanup(func() {
		if err := app.Stop(); err != nil {
			t.Logf("Stop: %v", err)
		}
	})

	// 等待就绪，带上限；同时监听 Run 的提前返回
	deadline := time.After(10 * time.Second)
	for !app.Server().Started() {
		select {
		case err := <-runErr:
			t.Fatalf("服务端启动失败，Run 提前返回: %v", err)
		case <-deadline:
			t.Fatal("10s 内服务端未就绪")
		case <-time.After(10 * time.Millisecond):
		}
	}

	cli := client.NewRpcClient(
		client.Debug(),
		client.WithHost(app.Server().Config().Address),
	)

	arith := test.NewArithCli(cli)
	arg := &test.Args{Num1: 1, Num2: 2, Flt: 0.0123}

	result, err := arith.Mul(arg)
	if err != nil {
		t.Fatalf("RPC 调用失败: %v", err)
	}
	if result == nil {
		t.Fatal("RPC 返回空结果")
	}

	t.Logf("Mul 结果: %v", result)
}

// freeAddr 向内核要一个当前空闲的端口。
//
// 关掉监听后到服务端真正绑定之间存在极小的竞争窗口，但比钉死端口可靠得多 ——
// 固定端口在开发机上必然与正在运行的服务冲突。
func freeAddr(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("申请空闲端口失败: %v", err)
	}
	defer ln.Close()

	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("解析端口失败: %v", err)
	}
	return ":" + port
}
