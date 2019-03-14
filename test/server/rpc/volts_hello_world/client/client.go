package main

import (
	"github.com/volts-dev/logger"
	"github.com/volts-dev/volts/client"
	"github.com/volts-dev/volts/test"
)

func main() {
	/*s := server.NewServer()
	s.RegisterName("Arith", new(Arith))
	//s.RegisterName("PBArith", new(PBArith), "")
	go s.Listen("tcp", "127.0.0.1:0")
	defer s.Close()
	time.Sleep(500 * time.Millisecond)
	*/
	addr := "127.0.0.1:16888" //s.Address().String()

	client := client.NewClient(client.DefaultOption)
	err := client.Connect("tcp", addr)
	if err != nil {
		logger.Panicf("failed to connect: %v", err)
	}

	//defer client.Close()
	args := &test.Args{
		Num1: 10,
		Num2: 20,
	}

	reply := &test.Reply{}
	err = client.Call("Arith.Mul", args, reply)
	if err != nil {
		logger.Panicf("failed to call: %v", err)
	}

	if reply.Num != 200 {
		logger.Panicf("expect 200 but got %d", reply.Num)
	}

	args.Num1 = 20
	err = client.Call("Arith.Mul", args, reply)
	if err != nil {
		logger.Panicf("failed to call: %v", err)
	}

	logger.Info("complete", reply)
}
