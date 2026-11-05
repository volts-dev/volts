package transport

import (
	"context"
	"errors"
	"net"
	"testing"

	"golang.org/x/net/proxy"
)

type captureProxyForward struct {
	addr string
	ctx  context.Context
	err  error
}

func (d *captureProxyForward) DialContext(ctx context.Context, _ string, addr string) (net.Conn, error) {
	d.addr, d.ctx = addr, ctx
	return nil, d.err
}

func TestProxyForwardOnlyDialsEntry(t *testing.T) {
	previous := proxyForward.Load()
	t.Cleanup(func() { proxyForward.Store(previous) })
	stop := errors.New("entry dial stopped")
	for _, raw := range []string{
		"http://127.0.0.1:1", "https://127.0.0.1:1", "socks5://127.0.0.1:1",
		"ss://chacha20-ietf:p@127.0.0.1:1",
		"ssr://127.0.0.1:1?method=chacha20-ietf&password=p&protocol=origin&obfs=plain",
	} {
		t.Run(raw, func(t *testing.T) {
			forward := &captureProxyForward{err: stop}
			SetProxyForwardDialer(forward)
			d, err := NewProxyDialer(raw, "")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			_, err = d.(proxy.ContextDialer).DialContext(ctx, "tcp", "destination.invalid:443")
			if !errors.Is(err, stop) {
				t.Fatalf("没有使用入口拨号器: %v", err)
			}
			if forward.addr != "127.0.0.1:1" || forward.ctx != ctx {
				t.Fatalf("只能绑定代理入口，且必须保留 context: addr=%s ctx=%v", forward.addr, forward.ctx)
			}
		})
	}
}

func TestProxyForwardResetRestoresFallback(t *testing.T) {
	previous := proxyForward.Load()
	t.Cleanup(func() { proxyForward.Store(previous) })
	SetProxyForwardDialer(nil)
	if proxyForward.Load() != nil {
		t.Fatal("nil 应恢复普通拨号")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (&proxyForwardDialer{fallback: &net.Dialer{}}).DialContext(ctx, "tcp", "127.0.0.1:1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("fallback 未保留取消: %v", err)
	}
}
