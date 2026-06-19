package pool

import (
	"net"
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

	// while we have conns check age/liveness and then return one, otherwise
	// we'll create a new conn. Each candidate is popped under the lock, but the
	// TTL and liveness checks run unlocked — the liveness probe does a short
	// blocking read and must not stall every other Get/Release on the pool.
	for {
		p.Lock()
		conns := p.conns[key]
		if len(conns) == 0 {
			p.Unlock()
			break
		}
		conn := conns[len(conns)-1]
		p.conns[key] = conns[:len(conns)-1]
		p.Unlock()

		// if conn is old kill it and move on
		if d := time.Since(conn.Created()); d > p.ttl {
			conn.IClient.Close()
			continue
		}

		// if the peer already closed the conn (server idle ReadTimeout or a
		// server restart), reusing it would make the next Recv return io.EOF.
		// Drop it and keep looking / dial a fresh one instead.
		if connClosedByPeer(conn.IClient) {
			conn.IClient.Close()
			continue
		}

		return conn, nil
	}

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

// connClosedByPeer reports whether the remote end has already closed conn. It
// runs a short non-blocking read probe on the underlying net.Conn:
//   - a timeout means the socket is idle but still alive  -> not closed
//   - io.EOF / reset / unexpected data means it is dead    -> closed
//
// A pooled RPC conn only sits idle between a completed response and the next
// request, so there is never legitimate unread data — anything readable is the
// peer's FIN/RST. Transports without a real net.Conn (in-memory/mock) cannot be
// probed and are assumed alive.
func connClosedByPeer(c transport.IClient) bool {
	nc := netConnOf(c)
	if nc == nil {
		return false
	}

	// Small positive deadline so the first read syscall is actually attempted
	// (an already-elapsed deadline would return immediately and miss a pending
	// EOF). The 1ms cost is only paid when reusing a genuinely live connection.
	if err := nc.SetReadDeadline(time.Now().Add(time.Millisecond)); err != nil {
		return true // can't probe safely -> treat as dead and dial fresh
	}
	defer nc.SetReadDeadline(time.Time{})

	var buf [1]byte
	n, err := nc.Read(buf[:])
	if err == nil {
		return n > 0 // unexpected payload on an idle conn -> out of sync, drop it
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return false // idle but alive
	}
	return true // EOF / reset / other -> closed
}

// netConnOf returns the underlying net.Conn of c, or nil if the transport does
// not expose one (recovering from transports whose Conn() is unimplemented).
func netConnOf(c transport.IClient) (nc net.Conn) {
	defer func() {
		if recover() != nil {
			nc = nil
		}
	}()
	return c.Conn()
}
