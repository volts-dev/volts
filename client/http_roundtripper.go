package client

import (
	"context"
	"crypto/tls"
	_errors "errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

type (
	roundTripper struct {
		sync.Mutex
		// fix typing
		JA3       string
		UserAgent string

		cachedConnections map[string]net.Conn
		cachedTransports  map[string]http.RoundTripper
		DialTLS           func(network, addr string) (net.Conn, error)
		Dialer            proxy.Dialer
	}
)

var errProtocolNegotiated = _errors.New("protocol negotiated")

func (rt *roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt.Dialer == nil {
		rt.Dialer = proxy.Direct
	}
	/*
		// Fix this later for proper cookie parsing
		for _, properties := range rt.Cookies {
			req.AddCookie(&http.Cookie{Name: properties.Name,
				Value:      properties.Value,
				Path:       properties.Path,
				Domain:     properties.Domain,
				Expires:    properties.JSONExpires.Time, //TODO: scuffed af
				RawExpires: properties.RawExpires,
				MaxAge:     properties.MaxAge,
				HttpOnly:   properties.HTTPOnly,
				Secure:     properties.Secure,
				SameSite:   properties.SameSite,
				Raw:        properties.Raw,
				Unparsed:   properties.Unparsed,
			})
		}*/
	req.Header.Set("User-Agent", rt.UserAgent)
	addr := rt.getDialTLSAddr(req)

	if rt.cachedTransports == nil {
		rt.cachedTransports = make(map[string]http.RoundTripper)
	}
	if rt.cachedConnections == nil {
		rt.cachedConnections = make(map[string]net.Conn)
	}

	if _, ok := rt.cachedTransports[addr]; !ok {
		if err := rt.getTransport(req, addr); err != nil {
			return nil, err
		}
	}
	return rt.cachedTransports[addr].RoundTrip(req)
}

func (rt *roundTripper) getDialTLSAddr(req *http.Request) string {
	host, port, err := net.SplitHostPort(req.URL.Host)
	if err == nil {
		return net.JoinHostPort(host, port)
	}
	return net.JoinHostPort(req.URL.Host, "443") // we can assume port is 443 at this point
}

func (rt *roundTripper) getTransport(req *http.Request, addr string) error {
	switch strings.ToLower(req.URL.Scheme) {
	case "http":
		rt.cachedTransports[addr] = &http.Transport{Dial: rt.Dialer.Dial, DisableKeepAlives: true}
		return nil
	case "https":
	default:
		return fmt.Errorf("invalid URL scheme: [%v]", req.URL.Scheme)
	}

	_, err := rt.dialTLS(context.Background(), "tcp", addr)
	switch err {
	case errProtocolNegotiated:
	case nil:
		// Should never happen.
		//panic("dialTLS returned no error when determining cachedTransports")
	default:
		return err
	}

	return nil
}

func (rt *roundTripper) dialTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	rt.Lock()
	defer rt.Unlock()

	// If we have the connection from when we determined the HTTPS
	// cachedTransports to use, return that.
	if conn := rt.cachedConnections[addr]; conn != nil {
		delete(rt.cachedConnections, addr)
		return conn, nil
	}
	conn, err := rt.DialTLS(network, addr)
	if err != nil {
		return nil, err
	}
	//////////
	if rt.cachedTransports[addr] != nil {
		return conn, nil
	}

	// No http.Transport constructed yet, create one based on the results
	// of ALPN.
	if c, ok := conn.(*utls.UConn); ok {
		switch c.ConnectionState().NegotiatedProtocol {
		case http2.NextProtoTLS:
			// The remote peer is speaking HTTP 2 + TLS.
			rt.cachedTransports[addr] = &http2.Transport{DialTLS: rt.dialTLSHTTP2}
		default:
			// Assume the remote peer is speaking HTTP 1.x + TLS.
			rt.cachedTransports[addr] = &http.Transport{DialTLSContext: rt.dialTLS}

		}
	}

	// Stash the connection just established for use servicing the
	// actual request (should be near-immediate).
	rt.cachedConnections[addr] = conn

	return nil, errProtocolNegotiated
}

func (rt *roundTripper) dialTLSHTTP2(network, addr string, _ *tls.Config) (net.Conn, error) {
	return rt.dialTLS(context.Background(), network, addr)
}
