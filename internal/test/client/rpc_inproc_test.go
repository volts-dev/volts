package client

import (
	"net"
	"testing"
	"time"

	"github.com/volts-dev/volts"
	"github.com/volts-dev/volts/client"
	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/internal/test"
	"github.com/volts-dev/volts/router"
	"github.com/volts-dev/volts/server"
	"github.com/volts-dev/volts/transport"
)

type (
	// ProbeCtrl 报告这次调用是从哪种连接进来的，并把收到的参数与请求头原样带回。
	ProbeCtrl struct{}

	probeReply struct {
		Kind   string
		Sum    int
		Header string
	}
)

func (ProbeCtrl) Probe(ctx *router.TRpcContext) {
	kind := "tcp"
	if c, ok := ctx.Request().Context.Value(transport.RemoteConnContextKey).(net.Conn); ok && c.RemoteAddr().Network() == "pipe" {
		kind = "pipe"
	}
	var arg test.Args
	if err := ctx.Request().Body().Decode(&arg); err != nil {
		ctx.Response().WriteStream(&probeReply{Kind: "decode error: " + err.Error()})
		return
	}
	ctx.Response().WriteStream(&probeReply{
		Kind:   kind,
		Sum:    arg.Num1 + arg.Num2,
		Header: ctx.Request().Header().Get("X-Probe"),
	})
}

// 同一个客户端调用本进程的服务：默认经进程内管道，DisableInproc 经 TCP。
// 走的是完整的一条路 —— 选点、连接池、帧编解码、路由、控制器 —— 两条路的结果必须
// 只差连接种类。这是"同进程与跨进程语义一致"在传输层的判据。
func TestRpcClient_LocalServerGoesInprocWithSameResult(t *testing.T) {
	r := router.New()
	r.Url([]string{"CONNECT"}, "Probe", new(ProbeCtrl))

	srv := server.New(server.WithRouter(r), server.Address(freeAddr(t)))
	app := volts.New(volts.Server(srv), volts.Transport(transport.NewTCPTransport()))
	runErr := make(chan error, 1)
	go func() { runErr <- app.Run() }()
	t.Cleanup(func() { _ = app.Stop() })

	deadline := time.After(10 * time.Second)
	for !app.Server().Started() {
		select {
		case err := <-runErr:
			t.Fatalf("服务端启动失败: %v", err)
		case <-deadline:
			t.Fatal("10s 内服务端未就绪")
		case <-time.After(10 * time.Millisecond):
		}
	}
	// Started 置位在 Serve 协程起跑之前；登记发生在 Serve 里，稍等一下。
	time.Sleep(50 * time.Millisecond)

	call := func(opts ...client.Option) probeReply {
		t.Helper()
		cli := client.NewRpcClient(append([]client.Option{client.WithHost(app.Server().Config().Address)}, opts...)...)
		req, err := cli.NewRequest("Probe", "/Probe.probe", &test.Args{Num1: 40, Num2: 2}, client.WithCodec(codec.MsgPack))
		if err != nil {
			t.Fatal(err)
		}
		req.Header().Add("X-Probe", "carried")
		rsp, err := cli.Call(req)
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		var out probeReply
		if err := rsp.Body().Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out
	}

	viaPipe := call()
	viaTCP := call(client.WithTransport(transport.NewTCPTransport(transport.DisableInproc())))

	if viaPipe.Kind != "pipe" {
		t.Fatalf("本进程的服务没有走进程内管道: %+v", viaPipe)
	}
	if viaTCP.Kind != "tcp" {
		t.Fatalf("DisableInproc 仍走了管道: %+v", viaTCP)
	}
	viaPipe.Kind, viaTCP.Kind = "", ""
	if viaPipe != viaTCP {
		t.Fatalf("管道与 TCP 结果不同:\n pipe=%+v\n tcp =%+v", viaPipe, viaTCP)
	}
	if viaPipe.Sum != 42 || viaPipe.Header != "carried" {
		t.Fatalf("参数或请求头没有送到: %+v", viaPipe)
	}
}
