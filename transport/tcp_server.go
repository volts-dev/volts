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

	// 登记为进程内直连目标（见 inproc.go）。TLS 监听不登记：管道上没有证书。
	cfg := self.transport.config
	if !cfg.Secure && cfg.TlsConfig == nil {
		unregister := registerInproc(self.listener.Addr(), func(conn net.Conn) {
			self.serveConn(hd, conn)
		})
		defer unregister()
	}

	var tempDelay time.Duration

	for {
		conn, err := self.listener.Accept()
		if err != nil {
			// EMFILE / ENFILE / similar 临时性资源耗尽错误也走 backoff —— 否则
			// fd 暂时耗尽就让 Serve 永久退出。Temporary() 在 Go 1.18 后被标记
			// deprecated，但 net 包内部仍用它表达此类瞬态条件，目前没有更好替代。
			if ne, ok := err.(net.Error); ok && (ne.Timeout() || ne.Temporary()) {
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

		go self.serveConn(hd, conn)
	}
}

// serveConn 服务一条连接上的全部消息，直到对端断开或读超时。
//
// 真 TCP 连接与进程内管道（inproc.go）走的是这同一个函数 —— 两条路的语义差别只能
// 出在 net.Conn 上，不能出在这里。
func (self *tcpTransportListener) serveConn(hd rpcHandler, conn net.Conn) {
	sock := NewTcpTransportSocket(conn, self.transport.config.ReadTimeout, self.transport.config.WriteTimeout)

	defer func() {
		if r := recover(); r != nil {
			log.Errf("rpc: panic serving %s: %v", conn.RemoteAddr(), r)
		}
		sock.Close()
	}()

	for {
		msg := GetMessageFromPool()

		// slowloris 防护：客户端必须在 ReadTimeout 内发起下一条消息
		// （正常请求或心跳）。设为 0 可关闭此保护，但会让恶意 idle 连接
		// 持有 fd 直到 OS 默认 keepalive 触发，不推荐。
		if rt := self.transport.config.ReadTimeout; rt > 0 {
			conn.SetReadDeadline(time.Now().Add(rt))
		} else {
			conn.SetReadDeadline(time.Time{})
		}

		// 等待读取客户端信号
		err := msg.Decode(conn)
		if err != nil {
			PutMessageToPool(msg)
			return // 连接断开(EOF)或读取错误，退出循环
		}

		ctx := context.WithValue(context.Background(), RemoteConnContextKey, conn)
		req := NewRpcRequest(ctx, msg, sock)
		rsp := NewRpcResponse(ctx, req, sock)

		hd.ServeRPC(rsp, req)
		PutMessageToPool(msg)
	}
}
