package transport

import (
	"bufio"
	"bytes"
	"fmt"
	"io/ioutil"
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
	}
)

func (h *httpTransportListener) Addr() net.Addr {
	return h.listener.Addr()
}

func (h *httpTransportListener) Close() error {
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
	srv := &http.Server{
		Handler: hd,
	}

	// insecure connection use h2c
	if !(h.ht.config.Secure || h.ht.config.TLSConfig != nil) {
		srv.Handler = h2c.NewHandler(hd, &http2.Server{})
	}

	// begin serving
	return srv.Serve(h.listener)
}

func (h *httpTransportListener) ___Serve(fn func(ISocket)) error {
	// create handler mux
	mux := http.NewServeMux()
	// register our transport handler
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var buf *bufio.ReadWriter
		var con net.Conn

		// read a regular request
		if r.ProtoMajor == 1 {
			b, err := ioutil.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			r.Body = ioutil.NopCloser(bytes.NewReader(b))
			// hijack the conn
			hj, ok := w.(http.Hijacker)
			if !ok {
				// we're screwed
				http.Error(w, "cannot serve conn", http.StatusInternalServerError)
				return
			}

			conn, bufrw, err := hj.Hijack()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			defer conn.Close()
			buf = bufrw
			con = conn
		}

		// buffered reader
		bufr := bufio.NewReader(r.Body)

		// save the request
		ch := make(chan *http.Request, 1)
		ch <- r

		// create a new transport socket
		h.sock = &HttpConn{
			ht:     h.ht,
			w:      w,
			r:      r,
			rw:     buf,
			buf:    bufr,
			ch:     ch,
			conn:   con,
			local:  h.Addr().String(),
			remote: r.RemoteAddr,
			closed: make(chan bool),
		}

		// execute the socket
		fn(h.sock)
	})

	// get optional handlers
	if h.ht.config.Context != nil {
		handlers, ok := h.ht.config.Context.Value("http_handlers").(map[string]http.Handler)
		if ok {
			for pattern, handler := range handlers {
				mux.Handle(pattern, handler)
			}
		}
	}

	// default http2 server
	srv := &http.Server{
		Handler: mux,
	}

	// insecure connection use h2c
	if !(h.ht.config.Secure || h.ht.config.TLSConfig != nil) {
		srv.Handler = h2c.NewHandler(mux, &http2.Server{})
	}

	// begin serving
	return srv.Serve(h.listener)
}

func (self *httpTransportListener) Sock() ISocket {
	return self.sock
}
