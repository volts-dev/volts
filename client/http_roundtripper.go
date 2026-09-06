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

	"github.com/volts-dev/volts/transport"
	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

type (
	roundTripper struct {
		// mu 只护住下面两张 map。RoundTrip 是被并发调用的（net/http 的契约），
		// 而此前它对两张 map 的判空、建表、读、写全在锁外 —— 竞态检测器一开就报
		// concurrent map writes，且真的会把进程打崩。
		mu sync.Mutex

		// buildMu 把「这个地址还没有 transport，去建一个」整段串起来。
		// 只靠 mu 不够：两个 goroutine 会同时判空、同时拨号，后拨的那条把先拨的
		// 连接从 cachedConnections 里挤掉，于是先拨的那根永远没人用也没人关。
		buildMu sync.Mutex

		// UserAgent 为空时**不动**请求里已有的 User-Agent。
		// 此前这里是无条件 Set，而构造方从来没填过这个字段 —— 结果是
		// client.HttpClient 每一个请求都带着 `User-Agent: ` 空值出去，
		// 把 call() 那边刚按 config.UserAgent / Ja3.UserAgent 算好的值抹掉。
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
	if rt.UserAgent != "" {
		req.Header.Set("User-Agent", rt.UserAgent)
	}
	addr := rt.getDialTLSAddr(req)

	if t := rt.transportFor(addr); t != nil {
		return t.RoundTrip(req)
	}

	rt.buildMu.Lock()
	t := rt.transportFor(addr)
	if t == nil {
		if err := rt.getTransport(req, addr); err != nil {
			rt.buildMu.Unlock()
			return nil, err
		}
		t = rt.transportFor(addr)
	}
	rt.buildMu.Unlock()

	if t == nil {
		// 建完了还是空 —— 说明 getTransport 走完了却没登记任何东西。
		// 直接 rt.cachedTransports[addr].RoundTrip(req) 会在 nil 接口上调方法，
		// 也就是在调用方的 goroutine 里 panic。宁可回一个能读的错误。
		return nil, fmt.Errorf("volts.client: 没能为 %s 建立传输层", addr)
	}
	return t.RoundTrip(req)
}

func (rt *roundTripper) transportFor(addr string) http.RoundTripper {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.cachedTransports[addr]
}

func (rt *roundTripper) setTransport(addr string, t http.RoundTripper) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.cachedTransports == nil {
		rt.cachedTransports = make(map[string]http.RoundTripper)
	}
	rt.cachedTransports[addr] = t
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
		rt.setTransport(addr, &http.Transport{Dial: rt.Dialer.Dial, DisableKeepAlives: true})
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
	rt.mu.Lock()
	// If we have the connection from when we determined the HTTPS
	// cachedTransports to use, return that.
	if conn := rt.cachedConnections[addr]; conn != nil {
		delete(rt.cachedConnections, addr)
		rt.mu.Unlock()
		return conn, nil
	}
	already := rt.cachedTransports[addr] != nil
	rt.mu.Unlock()

	conn, err := rt.DialTLS(network, addr)
	if err != nil {
		return nil, err
	}

	// transport 已经建好了，说明这次是 http.Transport 真的来要一根连接。
	if already {
		return conn, nil
	}

	// 还没有 transport，按 ALPN 结果建一个。
	//
	// 读 ALPN 走 transport.NegotiatedProtocolOf，它同时认 utls 的
	// NegotiatedProtocol() 和 crypto/tls 的 ConnectionState()。此前只认前者，
	// 而 *tls.Conn 不实现它 —— 于是「不配指纹的普通 HTTPS」这条路上 transport
	// 一次都没被登记过，RoundTrip 随后在 nil 接口上调方法，**每一个 HTTPS 请求
	// 都是 panic**（不是错误，是在调用方 goroutine 里崩）。
	proto, _ := transport.NegotiatedProtocolOf(conn)
	var t http.RoundTripper
	if proto == http2.NextProtoTLS {
		t = &http2.Transport{DialTLS: rt.dialTLSHTTP2}
	} else {
		// 读不出 ALPN 也按 HTTP/1.1 走：不确定的时候必须落到一个能用的默认值，
		// 不能什么都不登记。
		t = &http.Transport{DialTLSContext: rt.dialTLS}
	}

	rt.mu.Lock()
	if rt.cachedTransports == nil {
		rt.cachedTransports = make(map[string]http.RoundTripper)
	}
	rt.cachedTransports[addr] = t
	if rt.cachedConnections == nil {
		rt.cachedConnections = make(map[string]net.Conn)
	}
	// 顶掉一根旧的暂存连接就把它关掉：暂存的连接没有任何超时看管，漏一根就是
	// 一个直到进程退出都不会被回收的 fd。
	if old := rt.cachedConnections[addr]; old != nil {
		old.Close()
	}
	// Stash the connection just established for use servicing the
	// actual request (should be near-immediate).
	rt.cachedConnections[addr] = conn
	rt.mu.Unlock()

	return nil, errProtocolNegotiated
}

func (rt *roundTripper) dialTLSHTTP2(network, addr string, _ *tls.Config) (net.Conn, error) {
	return rt.dialTLS(context.Background(), network, addr)
}
