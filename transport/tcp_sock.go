package transport

import (
	"errors"
	"net"
	"time"
)

type tcpTransportSocket struct {
	conn net.Conn
	//transport ITransport

	// ReadTimeout sets readdeadline for underlying net.Conns
	ReadTimeout time.Duration
	// WriteTimeout sets writedeadline for underlying net.Conns
	WriteTimeout time.Duration

	//enc    *gob.Encoder
	//dec    *gob.Decoder
	//encBuf *bufio.Writer
}

func NewTcpTransportSocket(conn net.Conn, readTimeout time.Duration, writeTimeout time.Duration) *tcpTransportSocket {
	//encBuf := bufio.NewWriter(conn)
	return &tcpTransportSocket{
		conn:         conn,
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
		//transport: transport,
		//encBuf:    encBuf,
		//enc:       gob.NewEncoder(encBuf), // FIXME
		//dec:       gob.NewDecoder(conn),   // FIXME
	}
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
	if t.ReadTimeout > time.Duration(0) {
		t.conn.SetDeadline(time.Now().Add(t.ReadTimeout))
	}

	return m.Decode(t.conn) //return t.dec.Decode(&m)
}

func (t *tcpTransportSocket) Send(m *Message) error {
	// set timeout if its greater than 0
	if t.WriteTimeout > time.Duration(0) {
		t.conn.SetDeadline(time.Now().Add(t.WriteTimeout))
	}

	if _, err := t.conn.Write(m.Encode()); err != nil {
		return err
	}

	return nil
}

func (t *tcpTransportSocket) Close() error {
	return t.conn.Close()
}
