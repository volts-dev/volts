package pool

import (
	"testing"
	"time"

	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/transport"
)

type reuseHandler struct{}

func (*reuseHandler) String() string { return "pool-reuse-benchmark" }
func (h *reuseHandler) Handler() any { return h }
func (*reuseHandler) ServeRPC(w *transport.RpcResponse, r *transport.RpcRequest) {
	_, _ = w.Write(r.Message.Payload)
}

func benchmarkPool(b *testing.B, tcp bool) (*pool, string) {
	b.Helper()
	opts := []transport.Option{transport.ReadTimeout(time.Minute)}
	if tcp {
		opts = append(opts, transport.DisableInproc())
	}
	tr := transport.NewTCPTransport(opts...)
	ln, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = ln.Serve(&reuseHandler{}) }()
	b.Cleanup(func() {
		_ = ln.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			b.Error("listener did not stop")
		}
	})
	p := newPool(Config{Transport: tr, Size: 1, TTL: time.Hour})
	b.Cleanup(func() { _ = p.Close() })
	addr := ln.Addr().String()
	// Serve registers the in-process listener asynchronously.
	deadline := time.Now().Add(time.Second)
	for {
		c, err := p.Get(addr, transport.WithStream())
		if err != nil {
			b.Fatal(err)
		}
		if tcp || c.Conn().RemoteAddr().Network() == "pipe" {
			_ = p.Release(c, nil)
			break
		}
		_ = p.Release(c, transport.ErrMessageMalformed)
		if time.Now().After(deadline) {
			b.Fatal("in-process listener did not register")
		}
		time.Sleep(time.Millisecond)
	}
	return p, addr
}

func BenchmarkPoolReuse(b *testing.B) {
	for _, kind := range []string{"tcp", "inproc"} {
		b.Run(kind, func(b *testing.B) {
			p, addr := benchmarkPool(b, kind == "tcp")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				c, err := p.Get(addr, transport.WithStream())
				if err != nil {
					b.Fatal(err)
				}
				if err := p.Release(c, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkPooledRPCRoundTrip(b *testing.B) {
	for _, kind := range []string{"tcp", "inproc"} {
		b.Run(kind, func(b *testing.B) {
			p, addr := benchmarkPool(b, kind == "tcp")
			req, rsp := transport.GetMessageFromPool(), transport.GetMessageFromPool()
			defer transport.PutMessageToPool(req)
			defer transport.PutMessageToPool(rsp)
			req.SetMessageType(transport.MT_REQUEST)
			req.SetSerializeType(codec.JSON)
			req.Path, req.Payload = "/echo", []byte(`{"ok":true}`)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				c, err := p.Get(addr, transport.WithStream())
				if err != nil {
					b.Fatal(err)
				}
				if err = c.Send(req); err == nil {
					err = c.Recv(rsp)
				}
				_ = p.Release(c, err)
				if err != nil {
					b.Fatal(err)
				}
				if string(rsp.Payload) != string(req.Payload) {
					b.Fatal("echo payload changed")
				}
				rsp.Reset()
			}
		})
	}
}
