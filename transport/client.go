package client

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/rpc"
	"time"
)

const (
	//Failover selects another server automaticaly
	Failover FailMode = iota
	//Failfast returns error immediately
	Failfast
	//Failtry use current client again
	Failtry
	//Broadcast sends requests to all servers and Success only when all servers return OK
	Broadcast
	//Forking sends requests to all servers and Success once one server returns OK
	Forking
)

type (
	//FailMode is a feature to decide client actions when clients fail to invoke services
	FailMode int
	// SelectMode defines the algorithm of selecting a services from cluster
	SelectMode int

	// ClientCodecFunc is used to create a rpc.ClientCodecFunc from net.Conn.
	ClientCodecFunc func(conn io.ReadWriteCloser) rpc.ClientCodec

	clientCodecWrapper struct {
		rpc.ClientCodec
		//		PluginContainer IClientPluginContainer
		Timeout      time.Duration
		ReadTimeout  time.Duration
		WriteTimeout time.Duration
		Conn         net.Conn
	}

	//ClientSelector defines an interface to create a rpc.Client from cluster or standalone.
	IConnection interface {
		//Select returns a new client and it also update current client
		Select(clientCodecFunc ClientCodecFunc, options ...interface{}) (*rpc.Client, error)
		//SetClient set current client
		SetClient(*TClient)
		SetSelectMode(SelectMode)
		//AllClients returns all Clients
		AllClients(clientCodecFunc ClientCodecFunc) []*rpc.Client
	}

	// TConnection is used to a direct rpc server.
	// It don't select a node from service cluster but a specific rpc server.
	TConnection struct {
		Network     string
		Address     string
		DialTimeout time.Duration
		Client      *TClient
		rpc_client  *rpc.Client
	}

	IClient interface {
		Call(ctx context.Context, req IRequest, rsp interface{}, opts ...CallOption) error
	}

	TClient struct {
		TLSConfig *tls.Config

		//Timeout sets deadline for underlying net.Conns
		Timeout time.Duration
		//Timeout sets readdeadline for underlying net.Conns
		ReadTimeout time.Duration
		//Timeout sets writedeadline for underlying net.Conns
		WriteTimeout time.Duration

		FailMode   FailMode
		Connection IConnection
		CodecFunc  ClientCodecFunc
		Retries    int
	}
)

func wrapConn(c *TClient, clientCodecFunc ClientCodecFunc, conn net.Conn) (*rpc.Client, error) {
	//return rpc.NewClientWithCodec(clientCodecFunc(conn)), nil
	return rpc.NewClient(conn), nil
	/*	var ok bool
		if conn, ok = c.PluginContainer.DoPostConnected(conn); !ok {
			return nil, errors.New("failed to do post connected")
		}

		if c == nil || c.PluginContainer == nil {
			return rpc.NewClientWithCodec(clientCodecFunc(conn)), nil
		}

		wrapper := newClientCodecWrapper(c.PluginContainer, clientCodecFunc(conn), conn)
		wrapper.Timeout = c.Timeout
		wrapper.ReadTimeout = c.ReadTimeout
		wrapper.WriteTimeout = c.WriteTimeout

		return rpc.NewClientWithCodec(wrapper), nil
	*/
}

// NewDirectRPCClient creates a rpc client
func NewDirectRPCClient(c *TClient, clientCodecFunc ClientCodecFunc, network, address string, timeout time.Duration) (*rpc.Client, error) {
	//if network == "http" || network == "https" {
	switch network {
	case "http":
		//return NewDirectHTTPRPCClient(c, clientCodecFunc, network, address, "", timeout)
	case "kcp":
		//return NewDirectKCPRPCClient(c, clientCodecFunc, network, address, "", timeout)
	default:
	}

	var conn net.Conn
	var tlsConn *tls.Conn
	var err error

	if c != nil && c.TLSConfig != nil {
		dialer := &net.Dialer{
			Timeout: timeout,
		}
		tlsConn, err = tls.DialWithDialer(dialer, network, address, c.TLSConfig)
		//or conn:= tls.Client(netConn, &config)

		conn = net.Conn(tlsConn)
	} else {
		conn, err = net.DialTimeout(network, address, timeout)
	}

	if err != nil {
		return nil, err
	}

	return wrapConn(c, clientCodecFunc, conn)
}

func NewConnection(net string, srv string, timeout time.Duration) *TConnection {
	return &TConnection{
		Network:     net,
		Address:     srv,
		DialTimeout: timeout,
	}
}

//Select returns a rpc client.
func (self *TConnection) Select(clientCodecFunc ClientCodecFunc, options ...interface{}) (*rpc.Client, error) {
	if self.rpc_client != nil {
		return self.rpc_client, nil
	}
	c, err := NewDirectRPCClient(self.Client, clientCodecFunc, self.Network, self.Address, self.DialTimeout)
	self.rpc_client = c
	return c, err
}

//SetClient sets the unique client.
func (self *TConnection) SetClient(c *TClient) {
	self.Client = c
}

//SetSelectMode is meaningless for DirectClientSelector because there is only one client.
func (self *TConnection) SetSelectMode(sm SelectMode) {

}

//AllClients returns rpc.Clients to all servers
func (self *TConnection) AllClients(clientCodecFunc ClientCodecFunc) []*rpc.Client {
	if self.rpc_client == nil {
		return []*rpc.Client{}
	}
	return []*rpc.Client{self.rpc_client}
}

//NewClient create a client.
func NewClient(conn IConnection) *TClient {
	return &TClient{
		Connection: conn,
	}
}

//Call invokes the named function, waits for it to complete, and returns its error status.
func (self *TClient) Call(service_method string, args interface{}, reply interface{}) (err error) {
	/*	if self.FailMode == Broadcast {
			return self.clientBroadCast(serviceMethod, args, reply)
		}
		if self.FailMode == Forking {
			return self.clientForking(serviceMethod, args, reply)
		}
	*/

	//select a rpc.Client and call
	rpcClient, err := self.Connection.Select(self.CodecFunc, service_method, args)
	//selected
	if err == nil && rpcClient != nil {
		if err = rpcClient.Call(service_method, args, reply); err == nil {
			return //call successful
		}
	}

	// # 处理失败方法
	if self.FailMode == Failover {
		for retries := 0; retries < self.Retries; retries++ {
			rpcClient, err := self.Connection.Select(self.CodecFunc, service_method, args)
			if err != nil || rpcClient == nil {
				continue
			}

			err = rpcClient.Call(service_method, args, reply)
			if err == nil {
				return nil
			}
		}
	} else if self.FailMode == Failtry {
		for retries := 0; retries < self.Retries; retries++ {
			if rpcClient == nil {
				rpcClient, err = self.Connection.Select(self.CodecFunc, service_method, args)
			}
			if rpcClient != nil {
				err = rpcClient.Call(service_method, args, reply)
				if err == nil {
					return nil
				}
			}
		}
	}

	return
}

// Close closes the connection
func (self *TClient) Close() error {
	clients := self.Connection.AllClients(self.CodecFunc)

	if clients != nil {
		for _, client := range clients {
			client.Close()
		}
	}

	return nil
}
