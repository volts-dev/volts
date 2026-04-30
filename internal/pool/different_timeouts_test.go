package pool

import (
	"testing"
	"time"

	"github.com/volts-dev/volts/transport"
)

type mockTransport struct {
	transport.ITransport
	dialCount int
}

func (m *mockTransport) Dial(addr string, opts ...transport.DialOption) (transport.IClient, error) {
	m.dialCount++
	return &mockClient{addr: addr}, nil
}

func (m *mockTransport) Config() *transport.Config {
	return &transport.Config{
		DialTimeout:  15 * time.Second,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}
}

type mockClient struct {
	transport.IClient
	addr string
}

func (m *mockClient) Remote() string {
	return m.addr
}

func (m *mockClient) Close() error {
	return nil
}

func TestDifferentTimeoutsCaching(t *testing.T) {
	mt := &mockTransport{}
	p := newPool(Config{
		Transport: mt,
		Size:      10,
		TTL:       time.Minute,
	})

	addr := "127.0.0.1:8080"

	// 1. Get connection with 10s timeout
	c1, err := p.Get(addr, transport.WithReadTimeout(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if mt.dialCount != 1 {
		t.Fatalf("expected dial count 1, got %d", mt.dialCount)
	}

	// 2. Release it
	p.Release(c1, nil)

	// 3. Get connection with 20s timeout - should NOT reuse c1
	c2, err := p.Get(addr, transport.WithReadTimeout(20*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if mt.dialCount != 2 {
		t.Fatalf("expected dial count 2 (new dial for different timeout), got %d", mt.dialCount)
	}

	// 4. Get connection with 10s timeout again - should reuse c1
	c3, err := p.Get(addr, transport.WithReadTimeout(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if mt.dialCount != 2 {
		t.Fatalf("expected dial count 2 (reuse), got %d", mt.dialCount)
	}

	if c3.Key() != c1.Key() {
		t.Fatal("c3 should have the same key as c1")
	}

	// Release all
	p.Release(c2, nil)
	p.Release(c3, nil)
}
