package transport

import (
	"bufio"
	"context"
	"encoding/gob"
	"fmt"
	"net"
	"time"

	log "github.com/volts-dev/logger"
)

type (
	// contextKey is a value for use with context.WithValue. It's used as
	// a pointer so it fits in an interface{} without allocation.
	contextKey struct {
		name string
	}
	/*
		request struct {
			Header     protocol.Header
			Message    *protocol.TMessage
			RemoteAddr string

			// ctx is either the client or server context. It should only
			// be modified via copying the whole Request using WithContext.
			// It is unexported to prevent people from using Context wrong
			// and mutating the contexts held by callers of the same request.
			ctx context.Context
		}

		response struct {
			conn net.Conn
			req  *Request      // request for this response
			w    *bufio.Writer // buffers output in chunks to chunkWriter
			Val  reflect.Value
		}
	*/
	rpcHandler interface {
		ServeRPC(*RpcResponse, *RpcRequest)
	}

	tcpTransportListener struct {
		listener net.Listener
		timeout  time.Duration
		sock     *tcpTransportSocket
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

/*
// 写状态
func (self *response) WriteHeader(code protocol.MessageType) {
	self.req.Message.SetMessageType(code)
}
func (self *response) Write(data []byte) (n int, err error) {
	//n = len(data)
	return self.write(data)
}

func (self *response) write(data []byte) (n int, err error) {
	msg := self.req.Message
	msg.Payload = data

	self.WriteHeader(protocol.Response)
	data = msg.Encode()
	//n, err = self.conn.Write(data)
	return self.conn.Write(data)
}

func (w *response) finishRequest() {

}
func (self *response) Value() reflect.Value {
	return self.Val
}*/

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

func (t *tcpTransportListener) Serve(handler Handler) error {
	hd, ok := handler.Handler().(rpcHandler)
	if !ok {
		return fmt.Errorf("the handler is not a rpc handler! %v ", handler)
	}
	var tempDelay time.Duration

	for {
		conn, err := t.listener.Accept()
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

		encBuf := bufio.NewWriter(conn)
		t.sock = &tcpTransportSocket{
			timeout: t.timeout,
			conn:    conn,
			encBuf:  encBuf,
			enc:     gob.NewEncoder(encBuf),
			dec:     gob.NewDecoder(conn),
		}

		go func() {
			// TODO: think of a better error response strategy
			defer func() {
				if r := recover(); r != nil {
					t.sock.Close()
				}
			}()

			//@ 获取空白通讯包
			msg := GetMessageFromPool() // request message

			// TODO 自定义通讯包结构
			// 获得请求参数
			err = msg.Decode(conn) // 等待读取客户端信号
			if err != nil {
				//return err
			}

			ctx := context.WithValue(context.Background(), RemoteConnContextKey, conn)
			req := &RpcRequest{
				//Header:  *msg.Header,
				Message: msg,
				Context: ctx,
			}

			rsp := &RpcResponse{
				conn: conn,
				req:  req,
			}

			hd.ServeRPC(rsp, req)
		}()
	}
}
