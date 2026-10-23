package client

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/volts-dev/volts"
	"github.com/volts-dev/volts/client"
	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/router"
	"github.com/volts-dev/volts/server"
	"github.com/volts-dev/volts/transport"
)

// 按哈希取描述的整条线：TCP 服务器挂上框架保留端点，别的进程经 RPC 按哈希取回规范字节
// （registry/descriptor.go）。经 TCP 与经进程内管道各走一遍；取回来的字节必须正好哈希成
// 那个值 —— 读的一方就是靠这一点信任它的。
func TestDescribeEndpoint_ServesDescriptorsByDigest(t *testing.T) {
	r := router.New()
	srv := server.New(server.WithRouter(r), server.WithTransport(transport.NewTCPTransport()), server.Address(freeAddr(t)))
	app := volts.New(volts.Server(srv))
	runErr := make(chan error, 1)
	go func() { runErr <- app.Run() }()
	t.Cleanup(func() {
		_ = app.Stop()
		registry.SetDescriptorServing(false)
	})

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
	time.Sleep(50 * time.Millisecond)

	if !registry.DescriptorServing() {
		t.Fatal("TCP 服务器没有声明本进程答描述 —— 注册不会剥离，瘦身整个不生效")
	}
	for _, eps := range r.Endpoints() {
		for _, ep := range eps {
			if strings.Contains(ep.Path, registry.DescribeEndpoint) {
				t.Fatalf("描述端点被当成业务端点对外声明了：%s", ep.Path)
			}
		}
	}

	// 经解析层注册一个服务：它的描述进本进程的表，端点据此作答。
	svc := &registry.Service{
		Name: "probe_desc", Version: "latest",
		Metadata:  map[string]string{"depends": "base"},
		Endpoints: []*registry.Endpoint{{Name: "/probe_desc/a", Method: []string{"GET"}, Path: "/probe_desc/a"}},
		Nodes:     []*registry.Node{{Id: "probe-node", Address: app.Server().Config().Address}},
	}
	if err := registry.Use("memory").Register(svc); err != nil {
		t.Fatal(err)
	}
	_, digest, _ := registry.Describe(svc)

	fetch := func(opts ...client.Option) registry.DescribeResponse {
		t.Helper()
		cli := client.NewRpcClient(opts...)
		req, err := cli.NewRequest("volts.registry", registry.DescribeEndpoint,
			&registry.DescribeRequest{Digests: []string{digest, "0000"}}, client.WithCodec(codec.JSON))
		if err != nil {
			t.Fatal(err)
		}
		rsp, err := cli.Call(req, client.WithAddress(app.Server().Config().Address))
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		var out registry.DescribeResponse
		if err := rsp.Body().Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out
	}

	for name, out := range map[string]registry.DescribeResponse{
		"pipe": fetch(),
		"tcp":  fetch(client.WithTransport(transport.NewTCPTransport(transport.DisableInproc()))),
	} {
		raw, ok := out.Descs[digest]
		if !ok {
			t.Fatalf("[%s] 没交出那份描述：%v", name, out.Descs)
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != digest {
			t.Fatalf("[%s] 取回的字节哈希对不上 —— 读的一方会把它当成篡改丢掉", name)
		}
		if _, bogus := out.Descs["0000"]; bogus {
			t.Fatalf("[%s] 不认识的哈希也有回包", name)
		}
	}
}
