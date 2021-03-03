package rpc

import (
	"bufio"
	"net"
	"reflect"

	"github.com/volts-dev/volts/protocol"
)

type (
	Response interface {
		Write(b []byte) (n int, err error)
	}

	response struct {
		conn net.Conn
		req  *Request      // request for this response
		w    *bufio.Writer // buffers output in chunks to chunkWriter
		Val  reflect.Value
	}
)

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
}
