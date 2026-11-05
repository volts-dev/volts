package transport

import (
	"context"
	"net"
	"sync/atomic"

	"golang.org/x/net/proxy"
)

type proxyForwardSource struct{ dialer proxy.ContextDialer }

var proxyForward atomic.Pointer[proxyForwardSource]

// SetProxyForwardDialer sets the process-wide dialer for proxy entry connections.
// It never dials the destination inside the tunnel. nil restores normal routing.
// Existing connections are not moved; the dialer applies to new entry connections.
func SetProxyForwardDialer(d proxy.ContextDialer) {
	if d == nil {
		proxyForward.Store(nil)
		return
	}
	proxyForward.Store(&proxyForwardSource{dialer: d})
}

type proxyForwardDialer struct{ fallback *net.Dialer }

func (d *proxyForwardDialer) String() string { return "proxy-forward" }

func (d *proxyForwardDialer) Dial(network, addr string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, addr)
}

func (d *proxyForwardDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if source := proxyForward.Load(); source != nil {
		return source.dialer.DialContext(ctx, network, addr)
	}
	return d.fallback.DialContext(ctx, network, addr)
}
