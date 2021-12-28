package transport

import (
	"bufio"
	"encoding/gob"
	"errors"
	"net"
	"time"
)

type tcpTransportSocket struct {
	conn    net.Conn
	enc     *gob.Encoder
	dec     *gob.Decoder
	encBuf  *bufio.Writer
	timeout time.Duration
}

func (t *tcpTransportSocket) Conn() net.Conn {
	return t.conn
}

func (t *tcpTransportSocket) Local() string {
	return t.conn.LocalAddr().String()
}

func (t *tcpTransportSocket) Remote() string {
	return t.conn.RemoteAddr().String()
}

func (t *tcpTransportSocket) Recv(m *Message) error {
	if m == nil {
		return errors.New("message passed in is nil")
	}

	// set timeout if its greater than 0
	if t.timeout > time.Duration(0) {
		t.conn.SetDeadline(time.Now().Add(t.timeout))
	}

	return t.dec.Decode(&m)
}

func (t *tcpTransportSocket) Send(m *Message) error {
	// set timeout if its greater than 0
	if t.timeout > time.Duration(0) {
		t.conn.SetDeadline(time.Now().Add(t.timeout))
	}
	if err := t.enc.Encode(m); err != nil {
		return err
	}
	return t.encBuf.Flush()
}

func (t *tcpTransportSocket) Close() error {
	return t.conn.Close()
}
