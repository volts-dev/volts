package net

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/VectorsOrigin/logger"
)

// 替代原来错误提示
var ErrServerClosed = errors.New("rpc: Server closed")

type (

	// A dispatcher responds to an HTTP request.
	//
	// ServeHTTP should write reply headers and data to the ResponseWriter
	// and then return. Returning signals that the request is finished; it
	// is not valid to use the ResponseWriter or read from the
	// Request.Body after or concurrently with the completion of the
	// ServeHTTP call.
	//
	// Depending on the HTTP client software, HTTP protocol version, and
	// any intermediaries between the client and the Go server, it may not
	// be possible to read from the Request.Body after writing to the
	// ResponseWriter. Cautious handlers should read the Request.Body
	// first, and then reply.
	//
	// Except for reading the body, handlers should not modify the
	// provided Request.
	//
	// If ServeHTTP panics, the server (the caller of ServeHTTP) assumes
	// that the effect of the panic was isolated to the active request.
	// It recovers the panic, logs a stack trace to the server error log,
	// and either closes the network connection or sends an HTTP/2
	// RST_STREAM, depending on the HTTP protocol. To abort a dispatcher so
	// the client sees an interrupted response but the server doesn't log
	// an error, panic with the value ErrAbortHandler.
	IDispatcher interface {
		ServeTCP(net.Conn)
		ServeHTTP(w http.ResponseWriter, req *http.Request)
	}

	// Server is rpc server that use TCP or UDP.
	TServer struct {
		address      string
		network      string
		dispatcher   IDispatcher // dispatcher to invoke, http.DefaultServeMux if nil
		ln           net.Listener
		readTimeout  time.Duration
		writeTimeout time.Duration

		//serviceMapMu sync.RWMutex
		//serviceMap   map[string]*TModule // 存放模块路由

		mu         sync.RWMutex
		activeConn map[net.Conn]struct{}
		doneChan   chan struct{}
		seq        uint64

		// inShutdown int32
		onShutdown []func()

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

// NewServer returns a new Server.
func NewServer(network, address string, dispatcher IDispatcher) *TServer {
	return &TServer{
		address:    address,
		network:    strings.ToLower(network),
		dispatcher: dispatcher}
}

func ListenAndServe(network, address string, dispatcher IDispatcher) (*TServer, error) {
	s := NewServer(network, address, dispatcher)

	err := s.ListenAndServe()
	if err != nil {
		return nil, err
	}

	return s, nil
}

// ListenAndServe listens on the TCP network address srv.Addr and then
// calls Serve to handle requests on incoming connections.
// Accepted connections are configured to enable TCP keep-alives.
// If srv.Addr is blank, ":http" is used.
// ListenAndServe always returns a non-nil error.
func (self *TServer) ListenAndServe() error {
	ln, err := self.makeListener(self.network, self.address)
	if err != nil {
		return err
	}

	self.ln = ln
	return self.serve(ln)
}

func (self *TServer) Address() net.Addr {
	return self.ln.Addr()
}

func (s *TServer) serve(ln net.Listener) error {
	if s.network == "http" {
		// http 协议传输
		// serveByHTTP serves by HTTP.
		// if rpcPath is an empty string, use share.DefaultRPCPath.
		s.ln = ln

		//		if s.Plugins == nil {
		//			s.Plugins = &pluginContainer{}
		//		}

		//		if rpcPath == "" {
		//			rpcPath = share.DefaultRPCPath
		//		}
		//http.Handle("", s)
		srv := &http.Server{Handler: s.dispatcher}
		/*
			s.mu.Lock()
			if s.activeConn == nil {
				s.activeConn = make(map[net.Conn]struct{})
			}
			s.mu.Unlock()
		*/
		return srv.Serve(ln)
	}

	// try to start gateway
	/*ln = s.startGateway(network, ln)

	// 其他协议传输层
	if s.Plugins == nil {
		s.Plugins = &pluginContainer{}
	}
	*/
	var tempDelay time.Duration
	/*
		s.mu.Lock()
		s.ln = ln
		if s.activeConn == nil {
			s.activeConn = make(map[net.Conn]struct{})
		}
		s.mu.Unlock()
	*/
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

		//s.mu.Lock()
		//s.activeConn[conn] = struct{}{}
		//s.mu.Unlock()
		/*
			conn, ok := s.Plugins.DoPostConnAccept(conn)
			if !ok {
				continue
			}
		*/

		go s.serveConn(conn)
	}

	return nil
}
func (s *TServer) serveConn(conn net.Conn) {
	defer func() {
		/*
			if err := recover(); err != nil {
				const size = 64 << 10
				buf := make([]byte, size)
				ss := runtime.Stack(buf, false)
				if ss > size {
					ss = size
				}
				buf = buf[:ss]
				log.Errorf("serving %s panic error: %s, stack:\n %s", conn.RemoteAddr(), err, buf)
			}
			s.mu.Lock()
			delete(s.activeConn, conn)
			s.mu.Unlock()
		*/
		conn.Close()
	}()

	// 超时设定
	if tlsConn, ok := conn.(*tls.Conn); ok {
		if d := s.readTimeout; d != 0 {
			conn.SetReadDeadline(time.Now().Add(d))
		}
		if d := s.writeTimeout; d != 0 {
			conn.SetWriteDeadline(time.Now().Add(d))
		}
		if err := tlsConn.Handshake(); err != nil {
			log.Err("rpcx: TLS handshake error from %s: %v", conn.RemoteAddr(), err)
			return
		}
	}

	//r := bufio.NewReaderSize(conn, ReaderBuffsize)
	//w := bufio.NewWriterSize(conn, WriterBuffsize)

	//for {
	// 超时设定
	t0 := time.Now()
	if s.readTimeout != 0 {
		conn.SetReadDeadline(t0.Add(s.readTimeout))
	}

	/*  ctx
	ctx := context.WithValue(context.Background(), RemoteConnContextKey, conn)
	req, err := s.readRequest(ctx, r)
	if err != nil {
		if err == io.EOF {
			log.Infof("client has closed this connection: %s", conn.RemoteAddr().String())
		} else if strings.Contains(err.Error(), "use of closed network connection") {
			log.Infof("rpcx: connection %s is closed", conn.RemoteAddr().String())
		} else {
			log.Warnf("rpcx: failed to read request: %v", err)
		}
		return
	}
	*/
	if s.writeTimeout != 0 {
		conn.SetWriteDeadline(t0.Add(s.writeTimeout))
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

	s.dispatcher.ServeTCP(conn)
	/*
		go func() {
			if req.IsHeartbeat() {
				req.SetMessageType(protocol.Response)
				data := req.Encode()
				conn.Write(data)
				return
			}

			resMetadata := make(map[string]string)
			newCtx := context.WithValue(context.WithValue(ctx, share.ReqMetaDataKey, req.Metadata),
				share.ResMetaDataKey, resMetadata)

			res, err := s.handleRequest(newCtx, req)

			if err != nil {
				log.Warnf("rpcx: failed to handle request: %v", err)
			}

			//s.Plugins.DoPreWriteResponse(newCtx, req)
			if !req.IsOneway() {
				if len(resMetadata) > 0 { //copy meta in context to request
					meta := res.Metadata
					if meta == nil {
						res.Metadata = resMetadata
					} else {
						for k, v := range resMetadata {
							meta[k] = v
						}
					}
				}

				data := res.Encode()
				conn.Write(data)
				//res.WriteTo(conn)
			}
			//s.Plugins.DoPostWriteResponse(newCtx, req, res, err)

			//protocol.FreeMsg(req)
			//protocol.FreeMsg(res)
		}()*/
	//	}
}

// block can be nil if the caller wishes to skip encryption in kcp.
// tlsConfig can be nil iff we are not using network "quic".
func (s *TServer) makeListener(network, address string) (ln net.Listener, err error) {
	ml := listeners[network]
	if ml == nil {
		return nil, fmt.Errorf("can not make listener for %s", network)
	}
	return ml(s, address)
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
