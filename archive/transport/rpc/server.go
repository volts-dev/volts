package rpc

import (
	"net"
	"net/rpc"

	"github.com/volts-dev/micro/server"
)

type (
	TServer struct {
		server   *rpc.Server
		listener net.Listener
	}
)

// NewServer returns a new Server.
func NewServer() *TServer {
	return &TServer{
		server: rpc.NewServer(),
	}
}

func (self *TServer) Register(model interface{}, name ...string) {
	if len(name) > 0 {
		self.server.RegisterName(name[0], model)
	} else {
		self.server.Register(model)
	}
}

func (self *TServer) Listen(network_type, aAddr string) {
	var (
		err error
	)

	switch network_type {
	case "tcp":
		self.listener, err = net.Listen(network_type, aAddr)
	default:
		//
	}

	if err != nil {

	}

	go func() {
		for {
			conn, err := self.listener.Accept()
			if err != nil {
				continue
			}

			go self.server.ServeConn(conn)
		}
	}()
}

func (self *TServer) Close() error {
	return self.listener.Close()
}

// Address return the listening address.
func (self *TServer) Address() string {
	return self.listener.Addr().String()
}
