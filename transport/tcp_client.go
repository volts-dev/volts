package transport

import (
	"bufio"
	"net"
	"time"
)

type tcpTransportClient struct {
	transport *tcpTransport
	config    DialConfig
	conn      net.Conn
	//enc      *gob.Encoder
	//dec     *gob.Decoder
	encBuf *bufio.Writer
	//timeout time.Duration
}

func (t *tcpTransportClient) Conn() net.Conn {
	return t.conn
}

func (t *tcpTransportClient) Local() string {
	return t.conn.LocalAddr().String()
}

func (t *tcpTransportClient) Remote() string {
	return t.conn.RemoteAddr().String()
}

func (t *tcpTransportClient) Send(m *Message) error {
	// set timeout if its greater than 0
	if t.transport.config.WriteTimeout > time.Duration(0) {
		t.conn.SetDeadline(time.Now().Add(t.transport.config.WriteTimeout))
	}

	if _, err := t.conn.Write(m.Encode()); err != nil {
		return err
	}

	return t.encBuf.Flush()
}

func (t *tcpTransportClient) Recv(m *Message) error {
	// set timeout if its greater than 0
	if t.transport.config.ReadTimeout > time.Duration(0) {
		t.conn.SetDeadline(time.Now().Add(t.transport.config.ReadTimeout))
	}

	return m.Decode(t.conn)
}

func (t *tcpTransportClient) Close() error {
	return t.conn.Close()
}
