package test

import (
	"context"

	"github.com/volts-dev/logger"
	"github.com/volts-dev/volts/client"
	"github.com/volts-dev/volts/codec"
)

type (
	ArithCli struct {
		client.IClient
	}
)

func NewArithCli(cli client.IClient) *ArithCli {
	return &ArithCli{
		IClient: cli,
	}
}

func (self ArithCli) Mul(arg *Args) (result *Reply, err error) {
	service := "Arith.Mul"
	endpoint := "Test.Endpoint"
	address := "127.0.0.1:35999"

	cli := client.NewRpcClient(
		client.WithCodec(codec.MsgPack), // 默认传输编码
	)
	req := client.NewRpcRequest(service, endpoint, arg)
	req.SetContentType(codec.MsgPack) // 也可以在Req中制定传输编码
	req.Write(arg)

	// test calling remote address
	rsp, err := cli.Call(context.Background(), req, client.WithAddress(address))
	if err != nil {
		logger.Err("call with address error:", err)
		return nil, err
	}

	rsp.Body().ReadData(&result)
	return result, nil
}
