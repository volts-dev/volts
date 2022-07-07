package transport

import (
	"net"
)

type tcpTransportClient struct {
	tcpTransportSocket
	transport *tcpTransport
	config    DialConfig
	conn      net.Conn
}

func (t *tcpTransportClient) Transport() ITransport {
	return t.transport
}
