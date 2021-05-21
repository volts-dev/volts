package server

import (
	"bufio"
	"net"
	"reflect"

	"github.com/volts-dev/volts/transport"
)

type (
	IRpcResponse interface {
		Write(b []byte) (n int, err error)
	}
	RpcResponse struct {
		conn net.Conn
		req  *RpcRequest   // request for this response
		w    *bufio.Writer // buffers output in chunks to chunkWriter
		Val  reflect.Value
	}
)

// 写状态
func (self *RpcResponse) WriteHeader(code transport.MessageType) {
	self.req.Message.SetMessageType(code)
}
func (self *RpcResponse) Write(data []byte) (n int, err error) {
	//n = len(data)
	return self.write(data)
}

func (self *RpcResponse) write(data []byte) (n int, err error) {
	msg := self.req.Message
	msg.Payload = data

	self.WriteHeader(transport.MT_RESPONSE)
	data = msg.Encode()
	//n, err = self.conn.Write(data)
	return self.conn.Write(data)
}

func (w *RpcResponse) finishRequest() {

}
func (self *RpcResponse) Value() reflect.Value {
	return self.Val
}
