package router

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/volts-dev/volts/internal/addr"
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

// TODO 改名称
func RpcReverseProxy(ctx *TRpcContext) {}

// TODO 改名称
func HttpReverseProxy(ctx *THttpContext) {
	service, err := getService(ctx)
	if err != nil {
		ctx.WriteHeader(500)
		return
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

	if isWebSocket(ctx) {
		serveWebSocket(rp.Host, ctx.Response(), ctx.Request().Request)
		return
	}

	// 缓冲请求体，使 http.Transport 可以在 stale 连接时自动重试（含 POST/PUT）。
	// GetBody 是 http.Transport 内部重试的触发条件：有 GetBody 时，transport
	// 检测到 errServerClosedIdle 后会自动重建连接并重放请求，无需在 ErrorHandler
	// 里手动 retry（手动 retry 传入的是已消费 Body 的 outgoing request，POST 会丢 body）。
	inReq := ctx.Request().Request
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
		}
	}

	proxy := &httputil.ReverseProxy{
		Transport: reverseProxyTransport,
		Rewrite:   rewriteFn,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Errf("http: proxy error: %v | Method: %s | Path: %s | Target: %s", err, r.Method, r.URL.Path, service)
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

// getService returns the service for this request from the selector
func getService(ctx *THttpContext) (string, error) {
	// create a random selector
	next, err := ctx.Router().Config().Selector.Select(
		ctx.Handler().Service(),
		selector.WithFilter(selector.FilterLabel("protocol", "http", "https")),
	)
	if err != nil {
		return "", err
	}

	// get the next service node
	node, err := next()
	if err != nil {
		return "", err
	}

	protocol := "http"
	if node.Metadata != nil && node.Metadata["protocol"] == "https" {
		protocol = "https"
	}

	host := addr.LocalFormat(node.Address) // TODO 考虑整个框架统一化
	return fmt.Sprintf("%s://%s", protocol, host), nil
}

// serveWebSocket used to serve a web socket proxied connection
func serveWebSocket(host string, w http.ResponseWriter, r *http.Request) {
	req := new(http.Request)
	*req = *r

	if len(host) == 0 {
		http.Error(w, "invalid host", 500)
		return
	}

	// set x-forward-for
	if clientIP, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		if ips, ok := req.Header["X-Forwarded-For"]; ok {
			clientIP = strings.Join(ips, ", ") + ", " + clientIP
		}
		req.Header.Set("X-Forwarded-For", clientIP)
	}

	// connect to the backend host
	conn, err := net.Dial("tcp", host)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	// hijack the connection
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "failed to connect", 500)
		return
	}

	nc, _, err := hj.Hijack()
	if err != nil {
		return
	}

	defer nc.Close()
	defer conn.Close()

	if err = req.Write(conn); err != nil {
		return
	}

	errCh := make(chan error, 2)

	cp := func(dst io.Writer, src io.Reader) {
		_, err := io.Copy(dst, src)
		errCh <- err
	}

	go cp(conn, nc)
	go cp(nc, conn)

	<-errCh // Wait for first goroutine
	<-errCh // Wait for second goroutine to complete
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
