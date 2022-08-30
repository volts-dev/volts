package pool

import (
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/volts-dev/volts/transport"
)

type pool struct {
	sync.Mutex
	size  int
	ttl   time.Duration
	tr    transport.ITransport
	conns map[string][]*poolConn
}

type poolConn struct {
	transport.IClient
	id      string
	created time.Time
}

func newPool(cfg Config) *pool {
	return &pool{
		size:  cfg.Size,
		tr:    cfg.Transport,
		ttl:   cfg.TTL,
		conns: make(map[string][]*poolConn),
	}
}

func (p *pool) Close() error {
	p.Lock()
	for k, c := range p.conns {
		for _, conn := range c {
			conn.IClient.Close()
		}
		delete(p.conns, k)
	}
	p.Unlock()
	return nil
}

// NoOp the Close since we manage it
func (p *poolConn) Close() error {
	return nil
}

func (p *poolConn) Id() string {
	return p.id
}

func (p *poolConn) Created() time.Time {
	return p.created
}

func (p *pool) Get(addr string, opts ...transport.DialOption) (Conn, error) {
	p.Lock()
	conns := p.conns[addr]

	// while we have conns check age and then return one
	// otherwise we'll create a new conn
	for len(conns) > 0 {
		conn := conns[len(conns)-1]
		conns = conns[:len(conns)-1]
		p.conns[addr] = conns

		// if conn is old kill it and move on
		if d := time.Since(conn.Created()); d > p.ttl {
			conn.IClient.Close()
			continue
		}

		// we got a good conn, lets unlock and return it
		p.Unlock()

		return conn, nil
	}

	p.Unlock()

	// create new conn
	c, err := p.tr.Dial(addr, opts...)
	if err != nil {
		return nil, err
	}
	return &poolConn{
		IClient: c,
		id:      uuid.New().String(),
		created: time.Now(),
	}, nil
}

func (p *pool) Release(conn Conn, err error) error {
	// don't store the conn if it has errored
	if err != nil {
		return conn.(*poolConn).IClient.Close()
	}

	// otherwise put it back for reuse
	p.Lock()
	conns := p.conns[conn.Remote()]
	if len(conns) >= p.size {
		p.Unlock()
		return conn.(*poolConn).IClient.Close()
	}
	p.conns[conn.Remote()] = append(conns, conn.(*poolConn))
	p.Unlock()

	return nil
}
