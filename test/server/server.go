package main

import (
	"vectors/rpc/server"
	"vectors/rpc/test"
)

func main() {
	s := server.NewServer()
	s.RegisterName("Arith", new(test.Arith))
	//s.RegisterName("PBArith", new(PBArith), "")
	s.Listen("tcp", "127.0.0.1:5999")
}
