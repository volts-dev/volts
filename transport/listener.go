package transport

import (
	"context"
	"net"
	"net/http"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

type (
	transportListener struct {
		listener  net.Listener
		transport ITransport
		sock      ISocket // TODO 还没实现
		http      *http.Server
	}
)

func (self *transportListener) Addr() net.Addr {
	return self.listener.Addr()
}

func (self *transportListener) Close() error {
	if self.http != nil {
		err := self.http.Close()
		if err != nil {
			return err
		}
	}

	return self.listener.Close()
}

func (t *transportListener) Accept() (net.Conn, error) {
	return t.listener.Accept()
}

func (self *transportListener) Serve(handler Handler) error {
	switch v := handler.Handler().(type) {
	case http.Handler:
		self.http = &http.Server{
			Handler: v,
		}
		// default http2 server

		// insecure connection use h2c
		if !(self.transport.Config().Secure || self.transport.Config().TlsConfig != nil) {
			self.http.Handler = h2c.NewHandler(self.http.Handler, &http2.Server{})
		}

		// begin serving
		return self.http.Serve(self.listener)

	case customHandler:
		self.http = &http.Server{
			Handler: &customxx{
				hd: v,
			},
		}
		// default http2 server

		// insecure connection use h2c
		if !(self.transport.Config().Secure || self.transport.Config().TlsConfig != nil) {
			self.http.Handler = h2c.NewHandler(self.http.Handler, &http2.Server{})
		}

		// begin serving
		return self.http.Serve(self.listener)

	case rpcHandler:
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

			sock := NewTcpTransportSocket(conn, self.transport.Config().ReadTimeout, self.transport.Config().WriteTimeout)
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

				v.ServeRPC(rsp, req)
			}()
		}
	}

	return nil
}

func (self *transportListener) Sock() ISocket {
	return self.sock
}
