package net

import (
	"crypto/tls"
	//	"fmt"
	"net"
)

// TODO 变量名称
var listeners = make(map[string]MakeListener)

func init() {
	listeners["tcp"] = tcpMakeListener
	listeners["tcp4"] = tcpMakeListener
	listeners["tcp6"] = tcp4MakeListener
	listeners["http"] = tcp6MakeListener
}

// RegisterMakeListener registers a MakeListener for network.
func RegisterMakeListener(network string, ml MakeListener) {
	listeners[network] = ml
}

// MakeListener defines a listener generater.
type MakeListener func(s *TServer, address string) (ln net.Listener, err error)

func tcpMakeListener(s *TServer, address string) (ln net.Listener, err error) {
	if s.tlsConfig == nil {
		ln, err = net.Listen("tcp", address)
	} else {
		ln, err = tls.Listen("tcp", address, s.tlsConfig)
	}

	return ln, err
}

func tcp4MakeListener(s *TServer, address string) (ln net.Listener, err error) {
	if s.tlsConfig == nil {
		ln, err = net.Listen("tcp4", address)
	} else {
		ln, err = tls.Listen("tcp4", address, s.tlsConfig)
	}

	return ln, err
}

func tcp6MakeListener(s *TServer, address string) (ln net.Listener, err error) {
	if s.tlsConfig == nil {
		ln, err = net.Listen("tcp6", address)
	} else {
		ln, err = tls.Listen("tcp6", address, s.tlsConfig)
	}

	return ln, err
}
