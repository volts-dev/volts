package transport

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

type (
	transportListener struct {
		listener  net.Listener
		transport ITransport
		sock      ISocket // TODO 还没实现
		// mu 保护 http：Serve（写）与 Close（读）可能并发（见
		// httpTransportListener 同款说明）。
		mu   sync.Mutex
		http *http.Server
	}
)

func (self *transportListener) Addr() net.Addr {
	return self.listener.Addr()
}

func (self *transportListener) Close() error {
	self.mu.Lock()
	srv := self.http
	self.mu.Unlock()

	if srv != nil {
		// http.Server.Close 会连带关闭其正在 Serve 的 listener；随后的显式
		// listener.Close 必然得到 net.ErrClosed——这是收尾时序的假错误，
		// 不是资源泄漏，予以忽略（broker/http 的 Close 曾因此误报
		// "use of closed network connection"）。
		err := srv.Close()
		if lerr := self.listener.Close(); lerr != nil && !errors.Is(lerr, net.ErrClosed) && err == nil {
			err = lerr
		}
		return err
	}

	return self.listener.Close()
}

func (t *transportListener) Accept() (net.Conn, error) {
	return t.listener.Accept()
}

// serveHTTP 在锁内发布 srv 到 self.http（Close 并发读取）后开始服务。
func (self *transportListener) serveHTTP(srv *http.Server) error {
	// default http2 server

	// insecure connection use h2c
	if !(self.transport.Config().Secure || self.transport.Config().TlsConfig != nil) {
		srv.Handler = h2c.NewHandler(srv.Handler, &http2.Server{})
	}

	self.mu.Lock()
	self.http = srv
	self.mu.Unlock()

	// begin serving
	return srv.Serve(self.listener)
}

func (self *transportListener) Serve(handler Handler) error {
	switch v := handler.Handler().(type) {
	case http.Handler:
		// 此前这里连超时和头部上限都没设 —— 一条连接可以永远不发完请求头。
		srv := &http.Server{Handler: v}
		applyServerLimits(srv, self.transport.Config())
		return self.serveHTTP(srv)

	case customHandler:
		srv := &http.Server{Handler: &customxx{hd: v}}
		applyServerLimits(srv, self.transport.Config())
		return self.serveHTTP(srv)

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
