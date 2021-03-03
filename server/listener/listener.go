package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"time"
	//quicconn "github.com/marten-seemann/quic-conn"
)

type (
	IListeners interface {
		Serve(l net.Listener) error
		Close() error
		Shutdown(ctx context.Context) error
	}

	MakeListener func(config *tls.Config, address string) (ln net.Listener, err error)
)

// shutdownPollInterval is how often we poll for quiescence
// during Server.Shutdown. This is lower during tests, to
// speed up tests.
// Ideally we could find a solution that doesn't involve polling,
// but which also doesn't have a high runtime cost (and doesn't
// involve any contentious mutexes), but that is left as an
// exercise for the reader.
var ShutdownPollInterval = 500 * time.Millisecond

// TODO 变量名称
var listeners = make(map[string]MakeListener)

func init() {
	listeners["tcp"] = tcpMakeListener
	listeners["tcp4"] = tcpMakeListener
	listeners["tcp6"] = tcp4MakeListener
	listeners["rpc"] = tcp4MakeListener
	listeners["http"] = tcp4MakeListener
	//listeners["http3"] = http3Listener // the 3rd generation of http QUIC
}

// RegisterMakeListener registers a MakeListener for network.
func RegisterMakeListener(network string, ml MakeListener) {
	listeners[network] = ml
}

func NewListener(config *tls.Config, network, address string) (net.Listener, error) {
	fn := listeners[network]
	if fn == nil {
		return nil, fmt.Errorf("can not make listener for %s", network)
	}

	return fn(config, address)
}

func tcpMakeListener(config *tls.Config, address string) (ln net.Listener, err error) {
	if config == nil {
		ln, err = net.Listen("tcp", address)
	} else {
		ln, err = tls.Listen("tcp", address, config)
	}

	return ln, err
}

func tcp4MakeListener(config *tls.Config, address string) (ln net.Listener, err error) {
	if config == nil {
		ln, err = net.Listen("tcp4", address)
	} else {
		ln, err = tls.Listen("tcp4", address, config)
	}

	return ln, err
}

func tcp6MakeListener(config *tls.Config, address string) (ln net.Listener, err error) {
	if config == nil {
		ln, err = net.Listen("tcp6", address)
	} else {
		ln, err = tls.Listen("tcp6", address, config)
	}

	return ln, err
}

func http3Listener(config *tls.Config, address string) (ln net.Listener, err error) {
	if config == nil {
		return nil, errors.New("TLSConfig must be configured in server.Options")
	}
	//return quicconn.Listen("udp", address, config)
	return nil, nil
}
