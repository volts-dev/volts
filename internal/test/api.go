package test

import (
	"github.com/volts-dev/volts/client"
	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/logger"
)

type (
	// ArithCli 是 Arith 服务的 RPC 客户端封装。
	//
	// NOTE 这里持有的是 *client.RpcClient 而非 client.IClient：构造请求所需的
	// NewRequest 并不在 IClient 接口上（HttpClient 的签名是 method/url/data，
	// RpcClient 是 service/method/request，两者语义不同，无法直接归一）。
	// 这是 IClient 接口完整性的既有缺口，此处按实际类型持有以免绕开注入的客户端。
	ArithCli struct {
		cli *client.RpcClient
	}
)

func NewArithCli(cli *client.RpcClient) *ArithCli {
	return &ArithCli{cli: cli}
}

// Mul 通过注入的 client 调用远端 Arith.Mul。
//
// NOTE 此前这里做了两件让本 helper 完全失效的事：
//  1. 无视注入的 self.IClient，自己 client.NewRpcClient() 新建一个 ——
//     调用方通过 WithHost 配的地址、编码、超时全部作废
//  2. 硬编码 address = "127.0.0.1:35999"
//
// 结果是它永远打向开发机的 35999（kylin 的 HTTP 端口），拿回一段 HTTP 响应，
// 报 "wrong magic number: 72"（72 是 'H'）—— 与真正的原因毫无关联。
func (self ArithCli) Mul(arg *Args) (result *Reply, err error) {
	service := "Arith.Mul"
	// Path 取的是 NewRequest 的 method 参数（rpc_client.go: reqMsg.Path = req.Method()），
	// 必须与服务端路由树里的路径一致。控制器 Arith 的 Mul 注册为 "/Arith.mul"。
	endpoint := "/Arith.mul"

	req, err := self.cli.NewRequest(service, endpoint, arg,
		client.WithCodec(codec.MsgPack), // 默认传输编码
	)
	if err != nil {
		return nil, err
	}

	rsp, err := self.cli.Call(req)
	if err != nil {
		logger.Err("call error:", err)
		return nil, err
	}

	if err = rsp.Body().Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}
