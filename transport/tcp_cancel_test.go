package transport

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/volts-dev/volts/codec"
)

type cancelRPCHandler struct {
	call func(*RpcResponse, *RpcRequest)
}

func (h *cancelRPCHandler) String() string                         { return "cancel-test" }
func (h *cancelRPCHandler) Handler() any                           { return h }
func (h *cancelRPCHandler) ServeRPC(w *RpcResponse, r *RpcRequest) { h.call(w, r) }

func cancelTestConnection(t *testing.T, tcp bool, h *cancelRPCHandler, readTimeout time.Duration) net.Conn {
	t.Helper()
	tr := NewTCPTransport(ReadTimeout(readTimeout), WriteTimeout(time.Second), DisableInproc()).(*tcpTransport)
	if !tcp {
		client, server := net.Pipe()
		ln := &tcpTransportListener{transport: tr}
		done := make(chan struct{})
		go func() { defer close(done); ln.serveConn(h, server) }()
		t.Cleanup(func() {
			client.Close()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Error("pipe server did not exit")
			}
		})
		return client
	}
	ln, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go ln.Serve(h)
	c, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	if err != nil {
		ln.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(); ln.Close() })
	return c
}
func sendCancelFrame(t *testing.T, c net.Conn, budget time.Duration) {
	t.Helper()
	m := newMessage()
	m.SetMessageType(MT_REQUEST)
	m.SetSerializeType(codec.JSON)
	m.Path = "/cancel"
	m.Payload = []byte(`{}`)
	if budget > 0 {
		m.Header["Timeout"] = strconv.FormatInt(int64(budget), 10)
	}
	c.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := c.Write(m.Encode()); err != nil {
		t.Fatal(err)
	}
}

func TestTCPDisconnectCancelsExecutingHandler(t *testing.T) {
	for _, tcp := range []bool{false, true} {
		t.Run(map[bool]string{false: "pipe", true: "real_tcp"}[tcp], func(t *testing.T) {
			started, exited := make(chan struct{}), make(chan error, 1)
			h := &cancelRPCHandler{call: func(w *RpcResponse, r *RpcRequest) {
				close(started)
				select {
				case <-r.Context.Done():
					exited <- r.Context.Err()
				case <-time.After(2 * time.Second):
					exited <- errors.New("handler did not receive cancellation")
				}
			}}
			c := cancelTestConnection(t, tcp, h, time.Second)
			sendCancelFrame(t, c, time.Second)
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("handler not started")
			}
			c.Close()
			select {
			case err := <-exited:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("disconnect cancellation: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("handler still running after disconnect")
			}
		})
	}
}
func TestTCPWireBudgetExpiresHandlerWithoutDisconnect(t *testing.T) {
	exited := make(chan error, 1)
	h := &cancelRPCHandler{call: func(w *RpcResponse, r *RpcRequest) { <-r.Context.Done(); exited <- r.Context.Err() }}
	c := cancelTestConnection(t, false, h, time.Second)
	sendCancelFrame(t, c, 30*time.Millisecond)
	select {
	case err := <-exited:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("wire budget was not applied")
	}
}
func TestTCPIdleTimeoutDoesNotLimitExecutingHandler(t *testing.T) {
	h := &cancelRPCHandler{call: func(w *RpcResponse, r *RpcRequest) {
		select {
		case <-time.After(100 * time.Millisecond):
			w.Write([]byte("ok"))
		case <-r.Context.Done():
			w.Write([]byte("incorrect-cancel"))
		}
	}}
	c := cancelTestConnection(t, false, h, 20*time.Millisecond)
	sendCancelFrame(t, c, time.Second)
	c.SetReadDeadline(time.Now().Add(time.Second))
	response := newMessage()
	if err := response.Decode(c); err != nil {
		t.Fatal(err)
	}
	if string(response.Payload) != "ok" {
		t.Fatalf("connection idle timeout killed active handler: %s", response.Payload)
	}
}
func TestRPCRequestContextRejectsInvalidBudgets(t *testing.T) {
	for _, raw := range []string{"0", "-1", "oops", "999999999999999999999999"} {
		if _, _, err := rpcRequestContext(context.Background(), raw); err == nil {
			t.Fatalf("invalid budget accepted: %q", raw)
		}
	}
	base, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	ctx, done, err := rpcRequestContext(base, strconv.FormatInt(int64(time.Second), 10))
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	a, _ := base.Deadline()
	b, _ := ctx.Deadline()
	if !a.Equal(b) {
		t.Fatal("wire budget extended parent deadline")
	}
}

func TestTCPQueuedFrameDoesNotReceiveFreshExecutionBudget(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(finish) }) })
	var calls atomic.Int32
	h := &cancelRPCHandler{call: func(w *RpcResponse, r *RpcRequest) {
		if calls.Add(1) != 1 {
			t.Error("expired queued frame executed")
			return
		}
		close(started)
		select {
		case <-finish:
			_, _ = w.Write([]byte("first"))
		case <-r.Context.Done():
		}
	}}
	c := cancelTestConnection(t, false, h, time.Second)
	sendCancelFrame(t, c, time.Second)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first frame did not execute")
	}
	sendCancelFrame(t, c, 10*time.Millisecond)
	<-time.After(30 * time.Millisecond)
	release.Do(func() { close(finish) })
	c.SetReadDeadline(time.Now().Add(time.Second))
	first := newMessage()
	if err := first.Decode(c); err != nil || string(first.Payload) != "first" {
		t.Fatalf("first response: %s %v", first.Payload, err)
	}
	if err := newMessage().Decode(c); err == nil {
		t.Fatal("expired second frame returned a successful response")
	}
	if calls.Load() != 1 {
		t.Fatalf("handler executions=%d", calls.Load())
	}
}
