package transport

import (
	"bufio"
	"encoding/gob"
	"net"
	"time"
)

type tcpTransportClient struct {
	dialOpts DialConfig
	conn     net.Conn
	enc      *gob.Encoder
	dec      *gob.Decoder
	encBuf   *bufio.Writer
	timeout  time.Duration
}

func (t *tcpTransportClient) Local() string {
	return t.conn.LocalAddr().String()
}

func (t *tcpTransportClient) Remote() string {
	return t.conn.RemoteAddr().String()
}

func (t *tcpTransportClient) Send(m *Message) error {
	// set timeout if its greater than 0
	if t.timeout > time.Duration(0) {
		t.conn.SetDeadline(time.Now().Add(t.timeout))
	}
	if err := t.enc.Encode(m); err != nil {
		return err
	}
	return t.encBuf.Flush()
}

func (t *tcpTransportClient) Recv(m *Message) error {
	// set timeout if its greater than 0
	if t.timeout > time.Duration(0) {
		t.conn.SetDeadline(time.Now().Add(t.timeout))
	}
	return t.dec.Decode(&m)
}

func (t *tcpTransportClient) Close() error {
	return t.conn.Close()
}
