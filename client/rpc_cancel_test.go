package client

import (
	"context"
	stderrors "errors"
	"testing"
	"time"

	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/internal/pool"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/selector"
	"github.com/volts-dev/volts/transport"
)

type cancellationPool struct {
	pool.Pool
	get     func() (pool.Conn, error)
	release func(pool.Conn, error) error
}

func (p *cancellationPool) Get(string, ...transport.DialOption) (pool.Conn, error) { return p.get() }
func (p *cancellationPool) Release(c pool.Conn, e error) error                     { return p.release(c, e) }

type cancellationBase = pool.Conn

type cancellationConn struct {
	cancellationBase
	send func(*transport.Message) error
}

func (c *cancellationConn) Send(m *transport.Message) error { return c.send(m) }

func TestRPCCancelDuringAcquireClosesLateConnectionWithoutSend(t *testing.T) {
	getStarted, finishGet, released := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	conn := &cancellationConn{send: func(*transport.Message) error { t.Error("request sent after caller cancellation"); return nil }}
	cli := NewRpcClient()
	cli.pool = &cancellationPool{get: func() (pool.Conn, error) { close(getStarted); <-finishGet; return conn, nil }, release: func(_ pool.Conn, e error) error { released <- e; return nil }}
	req, err := cli.NewRequest("test", "/wait", map[string]string{"request": "payload"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := cli.call(ctx, &registry.Node{Address: "unused"}, req, CallOptions{RequestTimeout: time.Second})
		done <- err
	}()
	select {
	case <-getStarted:
	case <-time.After(time.Second):
		t.Fatal("acquisition not started")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled call succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("caller waited for non-cancellable pool")
	}
	close(finishGet)
	select {
	case err := <-released:
		if !stderrors.Is(err, context.Canceled) {
			t.Fatalf("late conn returned as healthy: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("late conn leaked")
	}
}

func TestRPCCancelKeepsFrameAndPayloadOwnedByIOUntilExit(t *testing.T) {
	entered := make(chan *transport.Message, 1)
	finish := make(chan struct{})
	exited := make(chan struct{})
	conn := &cancellationConn{send: func(m *transport.Message) error {
		defer close(exited)
		entered <- m
		<-finish
		return stderrors.New("injected slow IO ends")
	}}
	cli := NewRpcClient(WithSerializeType(codec.JSON))
	cli.pool = &cancellationPool{get: func() (pool.Conn, error) { return conn, nil }, release: func(pool.Conn, error) error { return nil }}
	req, err := cli.NewRequest("test", "/owned", []byte("original"), Encoded(true))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := cli.call(ctx, &registry.Node{Address: "unused"}, req, CallOptions{RequestTimeout: time.Second})
		done <- err
	}()
	var frame *transport.Message
	select {
	case frame = <-entered:
	case <-time.After(time.Second):
		t.Fatal("IO not started")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled call succeeded")
		}
	case <-time.After(time.Second):
		close(finish)
		t.Fatal("cancelled call waited for IO despite independent frame ownership")
	}
	// Call 已返回但替身 IO 尚未退出：归池的 Reset 会把 Path/Payload 清空。
	if frame.Path != "/owned" || string(frame.Payload) != "original" {
		close(finish)
		t.Fatal("frame returned to pool before IO exited")
	}
	req.Body().Data.Reset()
	req.Body().Data.WriteString("modified")
	if string(frame.Payload) != "original" {
		close(finish)
		t.Fatal("late IO still borrows mutable request body")
	}
	close(finish)
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("IO did not exit")
	}
}

func TestRPCCancelledRequestOptionsContextFailsBeforeSelection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cli := NewRpcClient()
	req, err := cli.NewRequest("unregistered", "/never", nil, func(o *RequestOptions) { o.Context = ctx })
	if err != nil {
		t.Fatal(err)
	}
	_, err = cli.Call(req)
	if err == nil || err.Error() != context.Canceled.Error() {
		t.Fatalf("cancelled request context ignored before selection: %v", err)
	}
}

type cancellationSelector struct {
	selector.ISelector
	entered, finish chan struct{}
}

func (s *cancellationSelector) Select(string, ...selector.SelectOption) (selector.Next, error) {
	close(s.entered)
	<-s.finish
	return nil, stderrors.New("late lookup ended")
}
func TestRPCCancelDuringServiceDiscoveryReturnsWithoutSending(t *testing.T) {
	s := &cancellationSelector{entered: make(chan struct{}), finish: make(chan struct{})}
	cli := NewRpcClient()
	cli.config.Selector = s
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := cli.NewRequest("blocked-discovery", "/never", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := cli.Call(req, WithContext(ctx)); done <- err }()
	select {
	case <-s.entered:
	case <-time.After(time.Second):
		t.Fatal("discovery not started")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled discovery succeeded")
		}
	case <-time.After(time.Second):
		close(s.finish)
		t.Fatal("caller still waits for discovery")
	}
	close(s.finish)
}
