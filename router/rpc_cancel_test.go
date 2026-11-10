package router

import (
	"context"
	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/transport"
	"testing"
)

type rpcCancelSocket struct{ transport.ISocket }

func (rpcCancelSocket) Send(*transport.Message) error { return nil }

func TestServeRPCPreservesBaseContextWithoutHeaders(t *testing.T) {
	r := New(WithConfig(NewConfig()))
	base, cancel := context.WithCancel(context.WithValue(context.Background(), "cancel-test", true))
	defer cancel()
	seen := false
	r.Url([]string{"CONNECT"}, "/context-probe", func(ctx *TRpcContext) {
		seen = true
		if ctx.Context().Value("cancel-test") != true {
			t.Error("headerless RPC lost base context")
		}
		cancel()
		if ctx.Context().Err() != context.Canceled {
			t.Error("headerless RPC detached base cancellation")
		}
	})
	msg := transport.GetMessageFromPool()
	defer transport.PutMessageToPool(msg)
	msg.SetMessageType(transport.MT_REQUEST)
	msg.SetSerializeType(codec.JSON)
	msg.Path = "/context-probe"
	sock := rpcCancelSocket{}
	req := transport.NewRpcRequest(base, msg, sock)
	rsp := transport.NewRpcResponse(base, req, sock)
	r.ServeRPC(rsp, req)
	if !seen {
		t.Fatal("RPC handler did not execute")
	}
}
