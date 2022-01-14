package router

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/volts-dev/volts/selector"
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
		serveWebSocket(rp.Host, ctx.Response(), ctx.Request())
		return
	}

	httputil.NewSingleHostReverseProxy(rp).ServeHTTP(ctx.Response(), ctx.Request())
}

// getService returns the service for this request from the selector
func getService(ctx *THttpContext) (string, error) {
	// create a random selector
	next := selector.Random(ctx.Handler().Services)

	// get the next service node
	s, err := next()
	if err != nil {
		return "", nil
	}

	// FIXME http/https
	return fmt.Sprintf("http://%s", s.Address), nil
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

	<-errCh
}

func isWebSocket(ctx *THttpContext) bool {
	contains := func(key, val string) bool {
		vv := strings.Split(ctx.Request().Header.Get(key), ",")
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
