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
		listener net.Listener
		ht       *httpTransport
		sock     *HttpConn // TODO 还没实现
		https    *http.Server
	}
)

func (self *customxx) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	self.hd.ServeHTTP(
		w,
		NewHttpRequest(r),
	)
}
func (h *httpTransportListener) Addr() net.Addr {
	return h.listener.Addr()
}

func (h *httpTransportListener) Close() error {
	err := h.https.Close()
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

		h.https = &http.Server{
			Handler: hd,
		}
		//return fmt.Errorf("the handler is not a http handler! %v ", handler)
	}

	if hd, ok := handler.Handler().(customHandler); ok {
		h.https = &http.Server{
			Handler: &customxx{
				hd: hd,
			},
		}
	}
	// default http2 server

	// insecure connection use h2c
	if !(h.ht.config.Secure || h.ht.config.TLSConfig != nil) {
		h.https.Handler = h2c.NewHandler(h.https.Handler, &http2.Server{})
	}

	// begin serving
	return h.https.Serve(h.listener)
}

func (self *httpTransportListener) Sock() ISocket {
	return self.sock
}
