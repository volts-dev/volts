package transport

import (
	"net"
	"net/http"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

type (
	customHandler interface {
		//ServeHTTP(ResponseWriter, *Request)
		ServeHTTP(w http.ResponseWriter, r *THttpRequest)
	}
	customxx struct {
		hd customHandler
	}
	httpTransportListener struct {
		listener  net.Listener
		transport *HttpTransport
		sock      *HttpConn // TODO 还没实现
		http      *http.Server
	}
)

func (self *customxx) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	self.hd.ServeHTTP(w, NewHttpRequest(r))
}

func (h *httpTransportListener) Addr() net.Addr {
	return h.listener.Addr()
}

func (h *httpTransportListener) Close() error {
	err := h.http.Close()
	if err != nil {
		return err
	}
	return h.listener.Close()
}

func (t *httpTransportListener) Accept() (net.Conn, error) {
	return t.listener.Accept()
}

func (h *httpTransportListener) Serve(handler Handler) error {
	if hd, ok := handler.Handler().(http.Handler); ok {
		h.http = &http.Server{
			Handler: hd,
			// slowloris 攻击防护
			ReadTimeout:  h.transport.config.ReadTimeout,
			WriteTimeout: h.transport.config.WriteTimeout,
		}
	}

	if hd, ok := handler.Handler().(customHandler); ok {
		h.http = &http.Server{
			Handler: &customxx{
				hd: hd,
			},
			// slowloris 攻击防护
			ReadTimeout:  h.transport.config.ReadTimeout,
			WriteTimeout: h.transport.config.WriteTimeout,
		}
	}
	// default http2 server

	// insecure connection use h2c
	if !(h.transport.config.Secure || h.transport.config.TlsConfig != nil) {
		h.http.Handler = h2c.NewHandler(h.http.Handler, &http2.Server{})
	}

	// begin serving
	return h.http.Serve(h.listener)
}

func (self *httpTransportListener) Sock() ISocket {
	return self.sock
}
