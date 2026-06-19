package pool

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/volts-dev/volts/transport"
)

// countingTransport wraps a real transport and counts how many times it dials,
// so a test can tell whether the pool reused a connection or opened a new one.
type countingTransport struct {
	transport.ITransport
	dials int32
}

func (c *countingTransport) Dial(addr string, opts ...transport.DialOption) (transport.IClient, error) {
	atomic.AddInt32(&c.dials, 1)
	return c.ITransport.Dial(addr, opts...)
}

// waitConnClosedByPeer polls conn until a read observes the peer's FIN/RST
// (a non-timeout error), instead of sleeping an arbitrary duration. It fails the
// test if the connection stays open past the deadline.
func waitConnClosedByPeer(t *testing.T, conn net.Conn) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	buf := make([]byte, 1)
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
		_, err := conn.Read(buf)
		if err == nil {
			t.Fatalf("unexpected data on idle probe connection")
		}
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			continue // still open, FIN not arrived yet
		}
		conn.SetReadDeadline(time.Time{})
		return // peer closed
	}
	t.Fatal("connection was not closed by peer within timeout")
}

// TestPool_Get_DiscardsConnClosedByPeer reproduces the "EOF right after the
// server (re)started" bug: a long-lived pool holds a connection whose peer has
// already closed it (idle ReadTimeout on the server, or a server restart). Get
// must not hand that dead connection back to the caller — doing so makes the
// next Recv return io.EOF.
func TestPool_Get_DiscardsConnClosedByPeer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 16)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()

	tr := &countingTransport{ITransport: transport.NewTCPTransport()}
	p := newPool(Config{
		Transport: tr,
		Size:      10,
		TTL:       time.Minute,
	})
	defer p.Close()

	addr := ln.Addr().String()

	// 1. Get a healthy connection and release it back into the pool.
	c1, err := p.Get(addr, transport.WithStream())
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if got := atomic.LoadInt32(&tr.dials); got != 1 {
		t.Fatalf("expected 1 dial after first Get, got %d", got)
	}
	if err := p.Release(c1, nil); err != nil {
		t.Fatalf("release: %v", err)
	}

	// 2. Server closes the connection it accepted (idle timeout / restart).
	sc := <-accepted
	sc.Close()

	// 3. Wait until the FIN has actually reached the pooled connection.
	waitConnClosedByPeer(t, c1.Conn())

	// 4. Get again. The pool must dial a fresh connection rather than return the
	//    dead one. A reused dead connection would EOF on the next Recv.
	c2, err := p.Get(addr, transport.WithStream())
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	defer p.Release(c2, nil)

	if got := atomic.LoadInt32(&tr.dials); got != 2 {
		t.Fatalf("pool returned a connection already closed by the peer (dials=%d); expected a fresh dial (dials=2)", got)
	}
}
