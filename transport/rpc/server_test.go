package rpc

import (
	"encoding/gob"
	"sync"
	"testing"
	"time"

	"github.com/volts-dev/micro/client/rpc"
	"github.com/volts-dev/micro/server/rpc"
)

var (
	serverObj         *server.TServer
	serverAddr        string
	serviceName       = "api/1.0"
	serviceMethodName = "api/1.0.HelloWorld"
	service           = new(Api)
	once              sync.Once
)

type (
	IGet interface {
		GetA() string
	}

	Args struct {
		A string
		B string
		C interface{}
	}

	Reply struct {
		C string `msg:"c"`
	}

	Api struct{}
)

func (self *Args) GetA() string {
	if r, ok := self.c.(*Reply); ok {

	}
	return self.A
}

func (self *Api) HelloWorld(args *Args, reply *Reply) error {
	reply.C = args.GetA()
	return nil
}

func (self *Api) Error(args *Args, reply *Reply) error {
	panic("ERROR")
}

func startServer() {
	gob.Register()
	serverObj = server.NewServer()
	serverObj.Register(service, serviceName)
	serverObj.Listen("tcp", "127.0.0.1:0")
	serverAddr = serverObj.Address()
}

func startClient(t *testing.T) {
	s := client.NewConnection("tcp", serverAddr, 10*time.Second)
	client := client.NewClient(s)
	defer client.Close()

	args := &Args{"hello", "world"}
	var reply Reply
	err := client.Call(serviceMethodName, args, &reply)
	if err != nil {
		t.Errorf("error for Arith: %d*%d, %v \n", args.A, args.B, err)
	}

	t.Logf("%v: %v + %v=%v \n", serviceMethodName, args.A, args.B, reply.C)

	/*
		conn, err := net.DialTimeout("tcp", serverAddr, time.Minute)
		if err != nil {
			t.Errorf("dialing: %v", err)
		}

		client := msgpackrpc.NewClient(conn)
		defer client.Close()

		args := &Args{7, 8}
		var reply Reply
		divCall := client.Go(serviceMethodName, args, &reply, nil)
		replyCall := <-divCall.Done // will be equal to divCall
		if replyCall.Error != nil {
			t.Errorf("error for Arith: %d*%d, %v \n", args.A, args.B, replyCall.Error)
		} else {
			t.Logf("Arith: %d*%d=%d \n", args.A, args.B, reply.C)
		}
	*/
}

func TestServe(t *testing.T) {
	once.Do(startServer)
	startClient(t)
}
