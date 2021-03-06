package client

import (
	"crypto/tls"
	"net"
	"time"

	log "github.com/volts-dev/logger"
)

func newDirectConn(c *TClient, network, address string) (net.Conn, error) {
	var conn net.Conn
	var tlsConn *tls.Conn
	var err error

	if c != nil && c.config.TLSConfig != nil {
		dialer := &net.Dialer{
			Timeout: c.config.ConnectTimeout,
		}
		tlsConn, err = tls.DialWithDialer(dialer, network, address, c.config.TLSConfig)
		//or conn:= tls.Client(netConn, &config)
		conn = net.Conn(tlsConn)
	} else {
		conn, err = net.DialTimeout(network, address, c.config.ConnectTimeout)
	}

	if err != nil {
		log.Warnf("failed to dial server: %v", err)
		return nil, err
	}

	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(3 * time.Minute)
	}

	return conn, nil
}
