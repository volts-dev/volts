package client

import (
	//	"context"
	//	"fmt"
	"testing"

	//	"time"
	"vectors/rpc/test"
)

func TestClient_IT(t *testing.T) {
	/*s := server.NewServer()
	s.RegisterName("Arith", new(Arith))
	//s.RegisterName("PBArith", new(PBArith), "")
	go s.Listen("tcp", "127.0.0.1:0")
	defer s.Close()
	time.Sleep(500 * time.Millisecond)
	*/
	addr := "127.0.0.1:5999" //s.Address().String()

	client := NewClient(DefaultOption)
	err := client.Connect("tcp", addr)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}

	//defer client.Close()
	args := &test.Args{
		Num1: 10,
		Num2: 20,
	}

	reply := &test.Reply{}
	err = client.Call("Arith.Mul", args, reply)
	if err != nil {
		t.Fatalf("failed to call: %v", err)
	}

	if reply.Num != 200 {
		t.Fatalf("expect 200 but got %d", reply.Num)
	}

	args.Num1 = 20
	err = client.Call("Arith.Mul", args, reply)
	if err != nil {
		t.Fatalf("failed to call: %v", err)
	}

	t.Log("complete", reply)

	/*
		err = client.Call("Arith.Mul", args, reply)
		if err != nil {
			t.Fatal("expect an error but got nil")
		}
	*/
	/*

		client.option.SerializeType = protocol.MsgPack
		reply = &Reply{}
		err = client.Call(context.Background(), "Arith", "Mul", args, reply)
		if err != nil {
			t.Fatalf("failed to call: %v", err)
		}

		if reply.C != 200 {
			t.Fatalf("expect 200 but got %d", reply.C)
		}

		client.option.SerializeType = protocol.ProtoBuffer

		pbArgs := &testutils.ProtoArgs{
			A: 10,
			B: 20,
		}
		pbReply := &testutils.ProtoReply{}
		err = client.Call(context.Background(), "PBArith", "Mul", pbArgs, pbReply)
		if err != nil {
			t.Fatalf("failed to call: %v", err)
		}

		if pbReply.C != 200 {
			t.Fatalf("expect 200 but got %d", pbReply.C)
		}
	*/
}
