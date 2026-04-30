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
	conns map[any][]*poolConn
}

type poolKey struct {
	addr         string
	dialTimeout  time.Duration
	readTimeout  time.Duration
	writeTimeout time.Duration
	secure       bool
	stream       bool
	network      string
}

type poolConn struct {
	transport.IClient
	id      string
	created time.Time
	key     any
}

func newPool(cfg Config) *pool {
	return &pool{
		size:  cfg.Size,
		tr:    cfg.Transport,
		ttl:   cfg.TTL,
		conns: make(map[any][]*poolConn),
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

// TODO 需要优化
func (p *pool) getPoolKey(addr string, opts ...transport.DialOption) poolKey {
	trCfg := p.tr.Config()
	dialCfg := transport.DialConfig{
		DialTimeout:  trCfg.DialTimeout,
		ReadTimeout:  trCfg.ReadTimeout,
		WriteTimeout: trCfg.WriteTimeout,
		Secure:       trCfg.Secure,
	}

	for _, o := range opts {
		if o != nil {
			o(&dialCfg)
		}
	}

	return poolKey{
		addr:         addr,
		dialTimeout:  dialCfg.DialTimeout,
		readTimeout:  dialCfg.ReadTimeout,
		writeTimeout: dialCfg.WriteTimeout,
		stream:       dialCfg.Stream,
		secure:       dialCfg.Secure,
		network:      dialCfg.Network,
	}
}

func (p *pool) Get(addr string, opts ...transport.DialOption) (Conn, error) {
	key := p.getPoolKey(addr, opts...)

	p.Lock()
	conns := p.conns[key]

	// while we have conns check age and then return one
	// otherwise we'll create a new conn
	for len(conns) > 0 {
		conn := conns[len(conns)-1]
		conns = conns[:len(conns)-1]
		p.conns[key] = conns

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
		key:     key,
	}, nil
}

func (p *pool) Release(conn Conn, err error) error {
	// don't store the conn if it has errored
	if err != nil {
		return conn.(*poolConn).IClient.Close()
	}

	// otherwise put it back for reuse
	p.Lock()
	key := conn.Key()
	conns := p.conns[key]
	if len(conns) >= p.size {
		p.Unlock()
		return conn.(*poolConn).IClient.Close()
	}
	p.conns[key] = append(conns, conn.(*poolConn))
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

func (p *poolConn) Key() any {
	return p.key
}
