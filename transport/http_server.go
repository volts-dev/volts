package transport

import (
	"fmt"
	"net"
	"net/http"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

type (
	httpTransportListener struct {
		listener net.Listener
		ht       *httpTransport
		sock     *HttpConn
		https    *http.Server
	}
)

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
	hd, ok := handler.Handler().(http.Handler)
	if !ok {
		return fmt.Errorf("the handler is not a http handler! %v ", handler)
	}
	// default http2 server
	h.https = &http.Server{
		Handler: hd,
	}

	// insecure connection use h2c
	if !(h.ht.config.Secure || h.ht.config.TLSConfig != nil) {
		h.https.Handler = h2c.NewHandler(hd, &http2.Server{})
	}

	// begin serving
	return h.https.Serve(h.listener)
}

func (self *httpTransportListener) Sock() ISocket {
	return self.sock
}
