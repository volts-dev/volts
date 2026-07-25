package router

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/volts-dev/volts/internal/addr"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/selector"
)

const (
	// maxProxyRetryBody 超过此大小的请求体不缓冲，不做自动重试
	maxProxyRetryBody = 1 << 20 // 1 MB
)

var (
	// reverseProxyTransport 是共享的出站连接池。
	//
	// 关键配置说明：
	// - ForceAttemptHTTP2 故意不设：HTTP/2 所有请求共用一条连接，
	//   该连接 EOF 时所有并发请求全部失败，且 CloseIdleConnections 对
	//   "正在使用中"的连接无效。HTTP/1.1 每个请求独立连接，故障隔离更好。
	// - IdleConnTimeout(60s) < 典型后端 keepalive(Nginx 75s)，
	//   确保本侧先淘汰空闲连接，避免复用后端已关闭的 stale 连接。
	// - ResponseHeaderTimeout 防止后端无响应时无限等待（调试断点常见场景）。
	reverseProxyTransport = &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second, // 防止调试断点导致无限挂起
	}
)

// RpcReverseProxy 是 RPC(CONNECT) 端点的代理占位实现 —— RPC 层的反向代理转发从未
// 实现过。
//
// ★ 它此前是一个空函数体：请求命中这条路由后什么都不做、也不写任何响应，连接就
// 那么挂着，调用方只能等满 RequestTimeout（默认 20s）拿到一句
// "context deadline exceeded"，而日志里没有任何线索指向这里。
//
// 现在 TRouter.store 不再为 CONNECT 端点建代理路由，正常路径不会走到这里。保留本
// 函数并让它立即失败，是为了万一还有其它路径引用：故障至少表现为一条能指认问题的
// 错误，而不是一次静默超时。跨进程 RPC 应由 client 依据注册中心直连目标服务节点。
func RpcReverseProxy(ctx *TRpcContext) {
	log.Errf("rpc: reverse proxy is not implemented | Service: %s — RPC 层不支持代理转发，调用方应通过 client 直连目标服务节点", ctx.Handler().Service())
	ctx.Abort("rpc reverse proxy is not implemented: call the target service node directly")
}

// TODO 改名称
func HttpReverseProxy(ctx *THttpContext) {
	inReq := ctx.Request().Request

	// ── 环防护①：跳数上限。与注册中心状态无关，为任何转发环（自环或跨节点
	//    A→B→A）封顶，使其永远无法耗尽临时端口。放在最前面：连节点都不必选。
	depth := proxyDepthOf(inReq)
	if depth >= maxProxyDepth {
		log.Errf("http: proxy loop detected, depth=%d ≥ %d | Method: %s | Path: %s — refusing to forward",
			depth, maxProxyDepth, inReq.Method, inReq.URL.Path)
		ctx.WriteHeader(http.StatusLoopDetected)
		return
	}

	service, node, err := getService(ctx)
	if err != nil {
		ctx.WriteHeader(500)
		return
	}

	// markNode 把本次代理的成败上报给 selector，供失败节点摘除/熔断使用。
	// 此前反向代理完全不上报，挂掉的上游会被持续选中直到注册中心 TTL 过期。
	markNode := func(err error) {
		if sel := ctx.Router().Config().Selector; sel != nil && node != nil {
			sel.Mark(ctx.Handler().Service(), node, err)
		}
	}

	if len(service) == 0 {
		ctx.WriteHeader(404)
		return
	}

	rp, err := url.Parse(service)
	if err != nil {
		ctx.WriteHeader(500)
		return
	}

	// ── 环防护②：自身地址短路。目标即本进程监听端口时绝不拨号 —— 这正是
	//    “反代到自己”导致 EADDRNOTAVAIL 的根因（注册中心抖动 / 同端口部署时发生）。
	//    自环不是节点故障，因此不上报 selector（既不摘除也不重置熔断）。
	if isSelfTarget(rp.Host, localListenAddr(inReq)) {
		log.Errf("http: proxy self-loop blocked | Method: %s | Path: %s | Target: %s == own listen addr — a discovered node advertises this gateway's own address; check registry / filteSelf",
			inReq.Method, inReq.URL.Path, service)
		ctx.WriteHeader(http.StatusLoopDetected)
		return
	}

	if isWebSocket(ctx) {
		// 必须把 scheme 一并传下去：后端是 https 时要走 TLS 拨号。
		// 此前只传 rp.Host，对 HTTPS 后端会以明文 TCP 直连 —— 握手必然失败，
		// 而 getService 明明已经解析出了 protocol。
		err := serveWebSocket(rp.Scheme, rp.Host, ctx.Response(), inReq)
		markNode(err)
		return
	}

	// 缓冲请求体，使 http.Transport 可以在 stale 连接时自动重试（含 POST/PUT）。
	// GetBody 是 http.Transport 内部重试的触发条件：有 GetBody 时，transport
	// 检测到 errServerClosedIdle 后会自动重建连接并重放请求，无需在 ErrorHandler
	// 里手动 retry（手动 retry 传入的是已消费 Body 的 outgoing request，POST 会丢 body）。
	var rewriteFn func(*httputil.ProxyRequest)
	if inReq.Body != nil && inReq.ContentLength > 0 && inReq.ContentLength <= maxProxyRetryBody {
		bodyBytes, err := io.ReadAll(inReq.Body)
		if err != nil {
			ctx.WriteHeader(http.StatusInternalServerError)
			return
		}
		inReq.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		rewriteFn = func(pr *httputil.ProxyRequest) {
			pr.SetURL(rp)
			pr.Out.Host = rp.Host
			pr.SetXForwarded()
			// 累计反代跳数，供下游（含本进程自己）的跳数护盾封顶转发环。
			pr.Out.Header.Set(proxyDepthHeader, strconv.Itoa(depth+1))
			// 为 outgoing request 设置 GetBody，让 transport 自动重试 stale 连接
			pr.Out.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			pr.Out.GetBody = func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(bodyBytes)), nil
			}
			pr.Out.ContentLength = int64(len(bodyBytes))
		}
	} else {
		rewriteFn = func(pr *httputil.ProxyRequest) {
			pr.SetURL(rp)
			pr.Out.Host = rp.Host
			pr.SetXForwarded()
			// 累计反代跳数，供下游（含本进程自己）的跳数护盾封顶转发环。
			pr.Out.Header.Set(proxyDepthHeader, strconv.Itoa(depth+1))
		}
	}

	proxy := &httputil.ReverseProxy{
		Transport: reverseProxyTransport,
		Rewrite:   rewriteFn,
		ModifyResponse: func(*http.Response) error {
			// 拿到上游响应即视为节点可达 —— 5xx 是业务语义，不代表节点故障，
			// 拿它去熔断会把一次业务异常放大成节点摘除。
			markNode(nil)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Errf("http: proxy error: %v | Method: %s | Path: %s | Target: %s", err, r.Method, r.URL.Path, service)
			// 连不上/超时/连接被重置才是节点级故障，上报给 selector。
			markNode(err)
			// ★ 关键修复：强制关闭客户端连接。
			// 不设此 header，Go HTTP server 会把这条连接放回 keep-alive 池，
			// 客户端复用该连接发下一个请求时读到损坏状态 → EOF。
			// 这是"一旦出现所有后续请求都 EOF"的真正根因。
			w.Header().Set("Connection", "close")
			if ctx.Response().Status() == 0 {
				ctx.WriteHeader(http.StatusBadGateway)
			}
		},
	}

	proxy.ServeHTTP(ctx.Response(), inReq)
}

// getService returns the service for this request from the selector.
//
// 同时返回选中的 node —— 调用方需要它把本次代理的成败上报给 selector
// （Mark），否则失败节点摘除/熔断拿不到任何信号。
func getService(ctx *THttpContext) (string, *registry.Node, error) {
	// create a random selector
	next, err := ctx.Router().Config().Selector.Select(
		ctx.Handler().Service(),
		selector.WithFilter(selector.FilterLabel("protocol", "http", "https")),
	)
	if err != nil {
		return "", nil, err
	}

	// get the next service node
	node, err := next()
	if err != nil {
		return "", nil, err
	}

	protocol := "http"
	if node.Metadata != nil && node.Metadata["protocol"] == "https" {
		protocol = "https"
	}

	host := addr.LocalFormat(node.Address) // TODO 考虑整个框架统一化
	return fmt.Sprintf("%s://%s", protocol, host), node, nil
}

// serveWebSocket used to serve a web socket proxied connection
//
// scheme 为 "https" 时以 TLS 拨号后端。返回的 error 用于上报节点健康度
// （nil 表示已成功接管连接）。
func serveWebSocket(scheme, host string, w http.ResponseWriter, r *http.Request) error {
	if len(host) == 0 {
		http.Error(w, "invalid host", 500)
		return errors.New("websocket proxy: empty host")
	}

	// 深拷贝请求：此前是 `req := new(http.Request); *req = *r` 的浅拷贝，
	// Header 仍与原请求共享同一个 map，下面的 Header.Set 会直接改写调用方
	// 持有的请求头。Clone 会连 Header/Trailer/URL 一并复制。
	req := r.Clone(r.Context())

	// 累计反代跳数：WebSocket 走 Hijack 直连，也要让跳数护盾能对 WS 转发环封顶。
	req.Header.Set(proxyDepthHeader, strconv.Itoa(proxyDepthOf(r)+1))

	// set x-forward-for
	if clientIP, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		if ips, ok := req.Header["X-Forwarded-For"]; ok {
			clientIP = strings.Join(ips, ", ") + ", " + clientIP
		}
		req.Header.Set("X-Forwarded-For", clientIP)
	}

	// connect to the backend host
	//
	// scheme 是 getService 依据节点 metadata["protocol"] 得出的。此前这里
	// 无条件明文 net.Dial —— 对 HTTPS 后端必然握手失败，等于 WebSocket
	// 反代在 TLS 后端上从来不可用。
	conn, err := dialBackend(scheme, host)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return err
	}

	// hijack the connection
	hj, ok := w.(http.Hijacker)
	if !ok {
		conn.Close() // 已建立的后端连接必须关掉，否则每次失败泄漏一条
		http.Error(w, "failed to connect", 500)
		return errors.New("websocket proxy: response writer is not a Hijacker")
	}

	nc, _, err := hj.Hijack()
	if err != nil {
		conn.Close()
		return err
	}

	defer nc.Close()
	defer conn.Close()

	if err = req.Write(conn); err != nil {
		return err
	}

	proxyBidir(conn, nc)
	return nil
}

// dialBackend 按 scheme 拨号后端。https 走 TLS，其余明文。
//
// TLS 握手带超时：没有超时的话，一个不响应的后端会让这条 goroutine
// 连同被劫持的客户端连接一起永久挂住。
func dialBackend(scheme, host string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second}

	if scheme != "https" {
		return dialer.Dial("tcp", host)
	}

	serverName := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		serverName = h
	}

	return tls.DialWithDialer(dialer, "tcp", host, &tls.Config{
		ServerName: serverName,
		MinVersion: tls.VersionTLS12,
	})
}

// proxyBidir copies bytes between a and b in both directions. It returns once
// both copy goroutines have finished. When the first direction finishes (for
// any reason — EOF, error, peer close), both conns are closed so the other
// direction's blocking Read unblocks immediately. Without this teardown, a
// half-closed connection (e.g. backend died but browser still alive and idle)
// would deadlock waiting for the second copy.
func proxyBidir(a, b net.Conn) {
	errCh := make(chan error, 2)

	cp := func(dst io.Writer, src io.Reader) {
		_, err := io.Copy(dst, src)
		errCh <- err
	}

	go cp(a, b)
	go cp(b, a)

	<-errCh
	// 关键：一侧 Copy 结束后必须主动 Close 双端，否则另一侧 Read 在仍活跃的 peer 上永久阻塞。
	a.Close()
	b.Close()
	<-errCh
}

func isWebSocket(ctx *THttpContext) bool {
	contains := func(key, val string) bool {
		vv := strings.Split(ctx.Request().Header().Get(key), ",")
		for _, v := range vv {
			if val == strings.ToLower(strings.TrimSpace(v)) {
				return true
			}
		}
		return false
	}

	if contains("Connection", "upgrade") && contains("Upgrade", "websocket") {
		return true
	}

	return false
}
