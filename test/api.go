package test

import (
	"github.com/volts-dev/volts/client"
	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/logger"
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

	cli := client.NewRpcClient()
	req, _ := cli.NewRequest(service, endpoint, arg,
		client.WithCodec(codec.MsgPack), // 默认传输编码
	)
	//req.SetContentType(codec.MsgPack) // 也可以在Req中制定传输编码
	//req.Write(arg)

	// test calling remote address
	rsp, err := cli.Call(req, client.WithAddress(address))
	if err != nil {
		logger.Err("call with address error:", err)
		return nil, err
	}

	rsp.Body().Decode(&result)
	return result, nil
}
