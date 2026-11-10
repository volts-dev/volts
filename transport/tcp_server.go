package transport

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
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
		// peer 是建监听那一刻的服务间 TLS（peer_tls.go），与 Secure 一样是这个监听
		// 一辈子的属性；nil = 只收明文。
		peer *PeerTLS
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
	// 服务间 TLS（peer_tls.go）不算：那是对**别的进程**的要求，本进程自己就是自己。
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

		if p := self.peer; p != nil {
			go func(conn net.Conn) {
				// 认首字节、握手都在这条连接自己的 goroutine 里：慢的对端不能卡住 Accept。
				if c, ok := acceptPeer(conn, p, self.transport.config.ReadTimeout); ok {
					self.serveConn(hd, c)
				}
			}(conn)
			continue
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
	base, stop := context.WithCancel(context.WithValue(context.Background(), RemoteConnContextKey, conn))
	type incomingRPC struct {
		message *Message
		ctx     context.Context
		cancel  context.CancelFunc
	}
	messages := make(chan incomingRPC) // 最多一个等待交付的帧，不建立无界请求队列。
	var mu sync.Mutex
	pending := 0
	setReadDeadline := func() {
		deadline := time.Time{}
		if rt := self.transport.config.ReadTimeout; pending == 0 && rt > 0 {
			deadline = time.Now().Add(rt)
		}
		_ = conn.SetReadDeadline(deadline)
	}
	defer func() {
		stop()
		_ = sock.Close()
		// socket 关闭使唯一读者退出；未交给 handler 的帧由这里/读者归还。
		for frame := range messages {
			frame.cancel()
			PutMessageToPool(frame.message)
		}
		if r := recover(); r != nil {
			log.Errf("rpc: panic serving %s: %v", conn.RemoteAddr(), r)
		}
	}()
	go func() {
		var held *Message
		var heldCancel context.CancelFunc
		defer func() {
			if heldCancel != nil {
				heldCancel()
			}
			if held != nil {
				PutMessageToPool(held)
			}
			if r := recover(); r != nil {
				log.Errf("rpc: panic reading %s: %v", conn.RemoteAddr(), r)
			}
			stop()
			close(messages)
		}()
		for {
			mu.Lock()
			setReadDeadline()
			mu.Unlock()
			held = GetMessageFromPool()
			if err := held.Decode(conn); err != nil {
				return
			}
			ctx, cancel, err := rpcRequestContext(base, held.Header["Timeout"])
			if err != nil {
				return
			}
			heldCancel = cancel // 预算从完整帧到达起算，排在前一个 handler 后面也不能重获预算。
			mu.Lock()
			pending++
			setReadDeadline()
			mu.Unlock()
			select {
			case messages <- incomingRPC{message: held, ctx: ctx, cancel: cancel}:
				held = nil
				heldCancel = nil
			case <-base.Done():
				return
			}
		}
	}()
	for frame := range messages {
		msg := frame.message
		if frame.ctx.Err() != nil {
			frame.cancel()
			PutMessageToPool(msg)
			return
		}
		func() {
			defer frame.cancel()
			defer PutMessageToPool(msg)
			req := NewRpcRequest(frame.ctx, msg, sock)
			rsp := NewRpcResponse(frame.ctx, req, sock)
			hd.ServeRPC(rsp, req)
		}()
		mu.Lock()
		pending--
		setReadDeadline()
		mu.Unlock()
	}
}

// 沿用 Timeout（纳秒）线格式。缺席的旧客户端仍无 handler 截止时间，但断连可取消。
// 不用读超时限制 handler：合法开户/上传的执行预算可长于连接空闲超时。
func rpcRequestContext(base context.Context, raw string) (context.Context, context.CancelFunc, error) {
	if raw == "" {
		return base, func() {}, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return nil, nil, fmt.Errorf("rpc: invalid Timeout header")
	}
	ctx, cancel := context.WithTimeout(base, time.Duration(n))
	return ctx, cancel, nil
}
