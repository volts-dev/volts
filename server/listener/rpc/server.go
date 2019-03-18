package rpc

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/volts-dev/volts/protocol"
	listener "github.com/volts-dev/volts/server/listener"

	log "github.com/volts-dev/logger"
)

// 替代原来错误提示
var ErrServerClosed = errors.New("rpc: Server closed")

// contextKey is a value for use with context.WithValue. It's used as
// a pointer so it fits in an interface{} without allocation.
type contextKey struct {
	name string
}

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

type (
	IDispatcher interface {
		ServeTCP(w Response, req *Request)
		ConnectBroke(w Response, req *Request)
	}

	// Server is rpc server that use TCP or UDP.
	TServer struct {
		address      string
		network      string
		Dispatcher   IDispatcher // dispatcher to invoke, http.DefaultServeMux if nil
		ln           net.Listener
		readTimeout  time.Duration
		writeTimeout time.Duration

		//serviceMapMu sync.RWMutex
		//serviceMap   map[string]*TModule // 存放模块路由

		mu            sync.RWMutex
		activeConn    map[net.Conn]struct{} // 连接池通过引用防止回收
		doneChan      chan struct{}
		seq           uint64
		handlerMsgNum int32
		inShutdown    int32
		onShutdown    []func()

		// TLSConfig for creating tls tcp connection.
		tlsConfig *tls.Config
		// BlockCrypt for kcp.BlockCrypt
		///options map[string]interface{}
		// // use for KCP
		// KCPConfig KCPConfig
		// // for QUIC
		// QUICConfig QUICConfig

		//Plugins PluginContainer

		// AuthFunc can be used to auth.
		//AuthFunc func(ctx context.Context, req *protocol.Message, token string) error
	}
)

func (s *TServer) checkProcessMsg() bool {
	size := s.handlerMsgNum
	log.Info("need handle msg size:", size)
	if size == 0 {
		return true
	}
	return false
}

// // Shutdown gracefully shuts down the server without interrupting any
// // active connections. Shutdown works by first closing the
// // listener, then closing all idle connections, and then waiting
// // indefinitely for connections to return to idle and then shut down.
// // If the provided context expires before the shutdown is complete,
// // Shutdown returns the context's error, otherwise it returns any
// // error returned from closing the Server's underlying Listener.
func (s *TServer) Shutdown(ctx context.Context) error {
	if atomic.CompareAndSwapInt32(&s.inShutdown, 0, 1) {
		log.Info("shutdown begin")
		ticker := time.NewTicker(listener.ShutdownPollInterval)
		defer ticker.Stop()
		for {
			if s.checkProcessMsg() {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
		}
		s.Close()
		log.Info("shutdown end")
	}
	return nil
}
func (s *TServer) Serve(ln net.Listener) error {

	// try to start gateway
	/*ln = s.startGateway(network, ln)

	// 其他协议传输层
	if s.Plugins == nil {
		s.Plugins = &pluginContainer{}
	}
	*/
	var tempDelay time.Duration

	// 初始化连接池
	//TODO 位置优化
	s.mu.Lock()
	if s.activeConn == nil {
		s.activeConn = make(map[net.Conn]struct{})
	}
	s.mu.Unlock()

	for {
		conn, e := ln.Accept()
		if e != nil {
			// 检测关闭
			select {
			case <-s.getDoneChan():
				return ErrServerClosed
			default:
			}

			// 错误信息分析
			if ne, ok := e.(net.Error); ok && ne.Temporary() {
				if tempDelay == 0 {
					tempDelay = 5 * time.Millisecond
				} else {
					tempDelay *= 2
				}

				if max := 1 * time.Second; tempDelay > max {
					tempDelay = max
				}

				log.Err("rpc: Accept error: %v; retrying in %v", e, tempDelay)
				time.Sleep(tempDelay)
				continue
			}
			return e
		}
		tempDelay = 0

		if tc, ok := conn.(*net.TCPConn); ok {
			tc.SetKeepAlive(true)
			tc.SetKeepAlivePeriod(3 * time.Minute)
		}

		// 存入池
		s.mu.Lock()
		s.activeConn[conn] = struct{}{}
		s.mu.Unlock()
		/*
			conn, ok := s.Plugins.DoPostConnAccept(conn)
			if !ok {
				continue
			}
		*/

		go s.serve(conn)
	}
}

// serve a connection
func (self *TServer) serve(conn net.Conn) {
	defer func() {
		if err := recover(); err != nil {
			const size = 64 << 10
			buf := make([]byte, size)
			ss := runtime.Stack(buf, false)
			if ss > size {
				ss = size
			}
			buf = buf[:ss]
			log.Errf("serving %s panic error: %s, stack:\n %s", conn.RemoteAddr(), err, buf)
		}
		self.mu.Lock()
		delete(self.activeConn, conn)
		self.mu.Unlock()

		conn.Close()

		log.Info("conne close")
	}()

	// 超时设定
	if tlsConn, ok := conn.(*tls.Conn); ok {
		if d := self.readTimeout; d != 0 {
			conn.SetReadDeadline(time.Now().Add(d))
		}
		if d := self.writeTimeout; d != 0 {
			conn.SetWriteDeadline(time.Now().Add(d))
		}
		if err := tlsConn.Handshake(); err != nil {
			log.Err("pcx: TLS handshake error from %s: %v", conn.RemoteAddr(), err)
			return
		}
	}

	//r := bufio.NewReaderSize(conn, ReaderBuffsize)
	//w := bufio.NewWriterSize(conn, WriterBuffsize)

	// 保持连接
	for {
		// 服务器关闭检测
		if isShutdown(self) {
			closeChannel(self, conn)
			return
		}

		// 超时设定
		t0 := time.Now()
		if self.readTimeout != 0 {
			conn.SetReadDeadline(t0.Add(self.readTimeout))
		}

		//  ctx
		ctx := context.WithValue(context.Background(), RemoteConnContextKey, conn)
		w, err := self.readRequest(ctx, conn)
		if err != nil {
			if err == io.EOF {
				log.Infof("client has closed this connection: %s", conn.RemoteAddr().String())
			} else if strings.Contains(err.Error(), "use of closed network connection") {
				log.Infof("rpc: connection %s is closed", conn.RemoteAddr().String())
			} else {
				log.Warnf("rpc: failed to read request: %v", err)
			}

			self.Dispatcher.ConnectBroke(w, w.req)
			return
		}
		if self.writeTimeout != 0 {
			conn.SetWriteDeadline(t0.Add(self.writeTimeout))
		}

		/*
			ctx = context.WithValue(ctx, StartRequestContextKey, time.Now().UnixNano())
			err = s.auth(ctx, req)
			if err != nil {
				s.Plugins.DoPreWriteResponse(ctx, req)
				if !req.IsOneway() {
					res := req.Clone()
					res.SetMessageType(protocol.Response)
					handleError(res, err)
					data := res.Encode()
					conn.Write(data)
					s.Plugins.DoPostWriteResponse(ctx, req, res, err)
					protocol.FreeMsg(res)
				}

				protocol.FreeMsg(req)
				continue
			}*/

		// 使用GO达到单连接异步处理
		go func() {
			// TODO　完善

			// 路由入口
			self.Dispatcher.ServeTCP(w, w.req)
			// save message to cache
			protocol.PutMessageToPool(w.req.Message)
			// TODO 添加中间件
		}()
	}
}

// Close immediately closes all active net.Listeners.
func (s *TServer) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeDoneChanLocked()
	var err error
	if s.ln != nil {
		err = s.ln.Close()
	}

	// 釋放連接池
	for c := range s.activeConn {
		c.Close()
		delete(s.activeConn, c)
	}
	return err
}

func (s *TServer) closeDoneChanLocked() {
	ch := s.getDoneChanLocked()
	select {
	case <-ch:
		// Already closed. Don't close again.
	default:
		// Safe to close here. We're the only closer, guarded
		// by s.mu.
		close(ch)
	}
}

func (s *TServer) getDoneChan() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.doneChan == nil {
		s.doneChan = make(chan struct{})
	}
	return s.doneChan
}

func (s *TServer) getDoneChanLocked() chan struct{} {
	if s.doneChan == nil {
		s.doneChan = make(chan struct{})
	}
	return s.doneChan
}

// TODO 修改函数名称 添加request独立返回值
func (self *TServer) readRequest(ctx context.Context, r net.Conn) (w *response, err error) {
	//@ 获取空白通讯包
	msg := protocol.GetMessageFromPool() // request message

	// TODO 自定义通讯包结构
	// 获得请求参数
	err = msg.Decode(r) // 等待读取客户端信号
	if err != nil {
		return nil, err
	}

	req := NewRequest(msg, ctx)

	w = &response{
		conn: r,
		req:  req,
	}

	return w, nil
}

func isShutdown(s *TServer) bool {
	return atomic.LoadInt32(&s.inShutdown) == 1
}

func closeChannel(s *TServer, conn net.Conn) {
	s.mu.Lock()
	delete(s.activeConn, conn)
	s.mu.Unlock()
	conn.Close()
}
