package client

import (
	stderrors "errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/volts-dev/volts"
	"github.com/volts-dev/volts/client"
	"github.com/volts-dev/volts/errors"
	"github.com/volts-dev/volts/internal/test"
	"github.com/volts-dev/volts/router"
	"github.com/volts-dev/volts/server"
	"github.com/volts-dev/volts/transport"
)

// 服务端把「路由不存在」写成 StatusNotFound + 空 body（router.ServeRPC），
// 客户端必须能把它识别成一次失败的调用。
//
// 回归的是这个 bug：rpc_client.call 组装 rpcResponse 时只填 contentType 和
// body，从不读 respMsg 的状态位、也不拷贝 respMsg.Header，随后把 respMsg 归还
// 消息池，状态彻底丢失 —— 调用方拿到 err=nil 的空结果，与「服务端正常返回空」
// 完全无法区分，上层网关据此回一个 HTTP 200 + 空 body。
func TestRpcClient_ServerNotFoundIsVisibleToCaller(t *testing.T) {
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

	if err == nil {
		t.Fatalf("服务端 StatusNotFound，客户端却返回 err=nil —— 与「正常返回空结果」无法区分（resp=%v）", resp)
	}

	// 错误必须带得出 HTTP 404，网关才能据此回一个正确的状态码而不是 500。
	var verr *errors.Error
	if !stderrors.As(err, &verr) {
		t.Fatalf("期望 *errors.Error，实得 %T: %v", err, err)
	}
	if verr.Code != http.StatusNotFound {
		t.Errorf("错误码 = %d，期望 %d(NotFound)；文案 = %q", verr.Code, http.StatusNotFound, verr.Detail)
	}
	if !strings.Contains(verr.Detail, "Arith.NoSuchMethod") {
		t.Errorf("错误文案未带上被调方法名，排查时无从定位: %q", verr.Detail)
	}

	// 响应体仍应返回给调用方（状态位场景下 body 为空，但不能是 nil resp）。
	if resp == nil {
		t.Fatal("出错时 resp 为 nil，调用方读不到服务端可能写回的诊断信息")
	}
	if resp.StatusCode() != int(transport.StatusNotFound) {
		t.Errorf("StatusCode() = %d，期望 %d(StatusNotFound)", resp.StatusCode(), transport.StatusNotFound)
	}
}
