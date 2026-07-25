package client

import (
	"testing"
	"time"

	"github.com/volts-dev/volts"
	"github.com/volts-dev/volts/client"
	"github.com/volts-dev/volts/internal/test"
	"github.com/volts-dev/volts/router"
	"github.com/volts-dev/volts/server"
	"github.com/volts-dev/volts/transport"
)

// 服务端把「路由不存在」写成 StatusNotFound + 空 body（router.ServeRPC），
// 客户端必须能把它识别成一次失败的调用。
//
// 现状：识别不了。rpc_client.call 组装 rpcResponse 时只填 contentType 和 body，
// 从不读 respMsg 的状态位、也不拷贝 respMsg.Header（handleError 写的
// __rpc_error__ 就在那里），随后把 respMsg 归还消息池，状态彻底丢失。于是
// rpcResponse.message 恒为 nil、StatusCode() 恒为 0(StatusOK)，Call 返回
// (resp, nil) —— 调用方拿到一个 error 为 nil 的空结果，与「服务端正常返回空」
// 完全无法区分。
//
// 本测试记录当前真实行为。修好状态透传后应改为断言 err != nil。
func TestRpcClient_ServerNotFoundIsInvisibleToCaller(t *testing.T) {
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

	runErr := make(chan error, 1)
	go func() { runErr <- app.Run() }()

	t.Cleanup(func() {
		if err := app.Stop(); err != nil {
			t.Logf("Stop: %v", err)
		}
	})

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
		client.WithHost(app.Server().Config().Address),
	)

	// 打一个服务端根本没有注册的方法 —— ServeRPC 会走 StatusNotFound 分支。
	req, err := cli.NewRequest("Arith", "Arith.NoSuchMethod", &test.Args{Num1: 1, Num2: 2})
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}

	resp, err := cli.Call(req)

	if err != nil {
		t.Logf("已能感知服务端错误（状态透传已修复）: %v", err)
		return
	}

	// —— 以下是当前（未修）行为的记录 ——
	t.Logf("★ 服务端 StatusNotFound，客户端 Call 返回 err = nil")
	if resp == nil {
		t.Fatal("err 和 resp 同时为空，调用方无从判断")
	}
	t.Logf("★ StatusCode() = %d（0=StatusOK；rpcResponse.message 从未被赋值）", resp.StatusCode())
	t.Logf("★ Header() = %v（respMsg.Header 从未拷贝，__rpc_error__ 丢失）", resp.Header())

	body := resp.Body().AsBytes()
	t.Logf("★ Body 长度 = %d 字节，内容 = %q", len(body), string(body))
	t.Log("★ 结论：一次「方法不存在」的调用，与「服务端正常返回空」在调用方看来完全一样")
}
