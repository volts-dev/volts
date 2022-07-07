package transport

import (
	"context"
	"fmt"
	"net"
	"time"
)

type (
	// contextKey is a value for use with context.WithValue. It's used as
	// a pointer so it fits in an interface{} without allocation.
	contextKey struct {
		name string
	}

	rpcHandler interface {
		ServeRPC(*RpcResponse, *RpcRequest)
	}

	tcpTransportListener struct {
		listener  net.Listener
		transport *tcpTransport
		sock      *tcpTransportSocket // TODO 还未实现
	}
)

func (k *contextKey) String() string { return "rpc context value " + k.name }

var (
	// RemoteConnContextKey is a context key. It can be used in
	// services with context.WithValue to access the connection arrived on.
	// The associated value will be of type net.Conn.
	RemoteConnContextKey = &contextKey{"remote-conn"}
	// StartRequestContextKey records the start time
	StartRequestContextKey = &contextKey{"start-parse-request"}
	// StartSendRequestContextKey records the start time
	StartSendRequestContextKey = &contextKey{"start-send-request"}
)

func (t *tcpTransportListener) Addr() net.Addr {
	return t.listener.Addr()
}

func (t *tcpTransportListener) Close() error {
	return t.listener.Close()
}

func (self *tcpTransportListener) Sock() ISocket {
	return self.sock
}

func (t *tcpTransportListener) Accept() (net.Conn, error) {
	return t.listener.Accept()
}

func (self *tcpTransportListener) Serve(handler Handler) error {
	hd, ok := handler.Handler().(rpcHandler)
	if !ok {
		return fmt.Errorf("the handler is not a rpc handler! %v ", handler)
	}
	var tempDelay time.Duration

	for {
		conn, err := self.listener.Accept()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Temporary() {
				if tempDelay == 0 {
					tempDelay = 5 * time.Millisecond
				} else {
					tempDelay *= 2
				}
				if max := 1 * time.Second; tempDelay > max {
					tempDelay = max
				}
				log.Errf("http: Accept error: %v; retrying in %v\n", err, tempDelay)
				time.Sleep(tempDelay)
				continue
			}
			return err
		}

		sock := NewTcpTransportSocket(conn, self.transport.config.ReadTimeout, self.transport.config.WriteTimeout)

		go func() {
			//@ 获取空白通讯包
			msg := GetMessageFromPool() // request message

			// TODO: think of a better error response strategy
			defer func() {
				if r := recover(); r != nil {
					sock.Close()
				}

				PutMessageToPool(msg)
			}()

			// TODO 自定义通讯包结构
			// 获得请求参数
			err = msg.Decode(conn) // 等待读取客户端信号
			if err != nil {
				//return err
				// TODO
			}

			ctx := context.WithValue(context.Background(), RemoteConnContextKey, conn)

			req := NewRpcRequest(ctx, msg, sock)
			rsp := NewRpcResponse(ctx, req, sock)

			hd.ServeRPC(rsp, req)
		}()
	}
}
