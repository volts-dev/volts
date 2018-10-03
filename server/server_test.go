package server

import (
	"context"
	"encoding/json"
	"testing"
	"vectors/rpc/codec"
	"vectors/rpc/protocol"

	"github.com/VectorsOrigin/logger"
)

type (
	Args struct {
		A int
		B int
	}

	Reply struct {
		C int
	}

	Arith int
)

func (t Arith) Mul(ctx context.Context, args *Args, reply *Reply) error {
	logger.Dbg("Mul", t, *args, *reply)

	reply.C = args.A * args.B
	logger.Dbg("Mul2", t, args, *reply)
	return nil
}

func Test_HandleRequest(t *testing.T) {
	//use jsoncodec
	server := NewServer()
	req := protocol.GetMessageFromPool() // request message
	//req := protocol.NewMessage()
	req.SetVersion(0)
	req.SetMessageType(protocol.Request)
	req.SetHeartbeat(false)
	req.SetOneway(false)
	req.SetCompressType(protocol.None)
	req.SetMessageStatusType(protocol.Normal)
	req.SetSerializeType(codec.JSON)
	req.SetSeq(1234567890)

	//req.ServicePath = "Arith"
	//req.ServiceMethod = "Mul"
	req.Path = "Arith.Mul"
	argv := &Args{
		A: 10,
		B: 20,
	}

	data, err := json.Marshal(argv)
	if err != nil {
		t.Fatal(err)
	}

	req.Payload = data
	module := NewModule()
	module.RegisterName("Arith", new(Arith))
	server.RegisterModule(module)
	server.Router.tree.PrintTrees()
	res, err := server.Router.handleRequest(req)
	if err != nil {
		t.Fatalf("failed to hand request: %v", err)
	}

	if res.Payload == nil {
		t.Fatalf("expect reply but got %s", res.Payload)
	}

	reply := &Reply{}

	code := codec.Codecs[res.SerializeType()]
	if code == nil {
		t.Fatalf("can not find codec %c", code)
	}
	t.Log(string(res.Payload), res.SerializeType())
	err = code.Decode(res.Payload, reply)
	if err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	t.Log(reply)
	if reply.C != 200 {
		t.Fatalf("expect 200 but got %d", reply.C)
	}

}
