package router

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/volts-dev/volts/selector"
)

var (
	// 定义一个全局的传输层，避免每个请求都新建，同时通过配置优化连接池
	reverseProxyTransport = &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second, // 建立连接超时
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          1024,
		MaxIdleConnsPerHost:   1024,             // 极其重要！默认只有 2，高并发下会导致大量连接重连导致 EOF
		IdleConnTimeout:       90 * time.Second, // 空闲连接超时
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableKeepAlives:     false,
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

	proxy := httputil.NewSingleHostReverseProxy(rp)
	proxy.Transport = reverseProxyTransport

	// 使用现代的 Rewrite 代替 Director
	proxy.Rewrite = func(r *httputil.ProxyRequest) {
		r.SetURL(rp)
		// 很多后端服务（如 Nginx 或严格的 Go Server）需要正确的 Host 头部
		r.Out.Host = rp.Host
		r.SetXForwarded() // 自动设置 X-Forwarded-For, X-Forwarded-Proto, X-Forwarded-Host
	}

	log.Dbgf("http: proxy: %s | Method: %s | Path: %s | Target: %s", ctx.Request().Method, ctx.Request().Request.URL.Path, service)
	// 添加错误处理，记录更多细节
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Errf("http: proxy error: %v | Method: %s | Path: %s | Target: %s", err, r.Method, r.URL.Path, service)
		// 这种错误通常意味着后端不可达或崩溃
		if ctx.Response().Status() == 0 {
			ctx.WriteHeader(http.StatusBadGateway)
		}
	}

	proxy.ServeHTTP(ctx.Response(), ctx.Request().Request)
}

// getService returns the service for this request from the selector
func getService(ctx *THttpContext) (string, error) {
	// create a random selector
	next := selector.Random(ctx.Handler().Services)

	// get the next service node
	s, err := next()
	if err != nil {
		return "", err
	}

	protocol := "http"
	if s.Metadata != nil && s.Metadata["protocol"] == "https" {
		protocol = "https"
	}

	return fmt.Sprintf("%s://%s", protocol, s.Address), nil
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
