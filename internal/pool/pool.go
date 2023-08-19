// Package pool is a connection pool
package pool

import (
	"time"

	"github.com/volts-dev/volts/transport"
)

// Pool is an interface for connection pooling
type Pool interface {
	// Close the pool
	Close() error
	// Get a connection
	Get(addr string, opts ...transport.DialOption) (Conn, error)
	// Releaes the connection
	Release(c Conn, status error) error
}

type Conn interface {
	// embedded connection
	transport.IClient
	// unique id of connection
	Id() string
	// time it was created
	Created() time.Time
}

func NewPool(opts ...Option) Pool {
	var cfg Config
	for _, o := range opts {
		o(&cfg)
	}
	return newPool(cfg)
}
