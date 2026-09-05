package transport

import (
	"errors"
	"net"
	"net/http"
	"sync"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

type (
	customHandler interface {
		ServeHTTP(http.ResponseWriter, *THttpRequest)
	}
	customxx struct {
		hd customHandler
	}
	httpTransportListener struct {
		listener  net.Listener
		transport *HttpTransport
		sock      *HttpConn // TODO 还没实现
		// mu 保护 http：Serve（写）与 Close（读）可能并发——broker 的
		// serve 重试循环会在 Close 进行中再次调用 Serve。
		mu   sync.Mutex
		http *http.Server
	}
)

func (self *customxx) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	self.hd.ServeHTTP(w, NewHttpRequest(r))
}

func (h *httpTransportListener) Addr() net.Addr {
	return h.listener.Addr()
}

func (h *httpTransportListener) Close() error {
	h.mu.Lock()
	srv := h.http
	h.mu.Unlock()

	// Serve 未被调用过时 srv 为 nil，直接关 listener 即可。
	if srv == nil {
		return h.listener.Close()
	}
	// http.Server.Close 会连带关闭其正在 Serve 的 listener；随后的显式
	// listener.Close 必然得到 net.ErrClosed——收尾时序的假错误而非资源
	// 泄漏，予以忽略（broker/http 的 Close 曾因此误报
	// "use of closed network connection"）。
	err := srv.Close()
	if lerr := h.listener.Close(); lerr != nil && !errors.Is(lerr, net.ErrClosed) && err == nil {
		err = lerr
	}
	return err
}

func (t *httpTransportListener) Accept() (net.Conn, error) {
	return t.listener.Accept()
}

func (h *httpTransportListener) Serve(handler Handler) error {
	// 先在局部构建完毕，再在锁内发布到 h.http（Close 并发读取）。
	var srv *http.Server
	if hd, ok := handler.Handler().(http.Handler); ok {
		srv = &http.Server{Handler: hd}
	}

	if hd, ok := handler.Handler().(customHandler); ok {
		srv = &http.Server{Handler: &customxx{hd: hd}}
	}
	applyServerLimits(srv, h.transport.config)
	// default http2 server

	// 明文连接上的 HTTP/2（h2c）改为显式开启 —— 见 Config.EnableH2C 的说明：
	// h2c 的明文升级在有前置代理的部署里是请求走私的经典载体，不该是默认行为。
	if h.transport.config.EnableH2C &&
		!(h.transport.config.Secure || h.transport.config.TlsConfig != nil) {
		srv.Handler = h2c.NewHandler(srv.Handler, &http2.Server{})
	}

	h.mu.Lock()
	h.http = srv
	h.mu.Unlock()

	// begin serving
	return srv.Serve(h.listener)
}

func (self *httpTransportListener) Sock() ISocket {
	return self.sock
}
