package transport

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"sync"
	"time"
)

type (
	HttpConn struct {
		ht *httpTransport
		w  http.ResponseWriter
		r  *http.Request
		rw *bufio.ReadWriter

		mtx sync.RWMutex

		// the hijacked when using http 1
		conn net.Conn
		// for the first request
		ch chan *http.Request

		// h2 things
		buf *bufio.Reader
		// indicate if socket is closed
		closed chan bool

		// local/remote ip
		local  string
		remote string
	}
)

func (h *HttpConn) Local() string {
	return h.local
}

func (h *HttpConn) Remote() string {
	return h.remote
}

func (h *HttpConn) Recv(m *Message) error {
	if m == nil {
		return errors.New("message passed in is nil")
	}
	if m.Header == nil {
		m.Header = make(map[string]string, len(h.r.Header))
	}

	// process http 1
	if h.r.ProtoMajor == 1 {
		// set timeout if its greater than 0
		if h.ht.config.Timeout > time.Duration(0) {
			h.conn.SetDeadline(time.Now().Add(h.ht.config.Timeout))
		}

		var r *http.Request

		select {
		// get first request
		case r = <-h.ch:
		// read next request
		default:
			rr, err := http.ReadRequest(h.rw.Reader)
			if err != nil {
				return err
			}
			r = rr
		}

		// read body
		b, err := ioutil.ReadAll(r.Body)
		if err != nil {
			return err
		}

		// set body
		r.Body.Close()
		m.Body = b

		// set headers
		for k, v := range r.Header {
			if len(v) > 0 {
				m.Header[k] = v[0]
			} else {
				m.Header[k] = ""
			}
		}

		// return early early
		return nil
	}

	// only process if the socket is open
	select {
	case <-h.closed:
		return io.EOF
	default:
		// no op
	}

	// processing http2 request
	// read streaming body

	// set max buffer size
	// TODO: adjustable buffer size
	buf := make([]byte, 4*1024*1024)

	// read the request body
	n, err := h.buf.Read(buf)
	// not an eof error
	if err != nil {
		return err
	}

	// check if we have data
	if n > 0 {
		m.Body = buf[:n]
	}

	// set headers
	for k, v := range h.r.Header {
		if len(v) > 0 {
			m.Header[k] = v[0]
		} else {
			m.Header[k] = ""
		}
	}

	// set path
	m.Header[":path"] = h.r.URL.Path

	return nil
}

func (h *HttpConn) Send(m *Message) error {
	if h.r.ProtoMajor == 1 {
		// make copy of header
		hdr := make(http.Header)
		for k, v := range h.r.Header {
			hdr[k] = v
		}

		rsp := &http.Response{
			Header:        hdr,
			Body:          ioutil.NopCloser(bytes.NewReader(m.Body)),
			Status:        "200 OK",
			StatusCode:    200,
			Proto:         "HTTP/1.1",
			ProtoMajor:    1,
			ProtoMinor:    1,
			ContentLength: int64(len(m.Body)),
		}

		for k, v := range m.Header {
			rsp.Header.Set(k, v)
		}

		// set timeout if its greater than 0
		if h.ht.config.Timeout > time.Duration(0) {
			h.conn.SetDeadline(time.Now().Add(h.ht.config.Timeout))
		}

		return rsp.Write(h.conn)
	}

	// only process if the socket is open
	select {
	case <-h.closed:
		return io.EOF
	default:
		// no op
	}

	// we need to lock to protect the write
	h.mtx.RLock()
	defer h.mtx.RUnlock()

	// set headers
	for k, v := range m.Header {
		h.w.Header().Set(k, v)
	}

	// write request
	_, err := h.w.Write(m.Body)

	// flush the trailers
	h.w.(http.Flusher).Flush()

	return err
}

func (h *HttpConn) error(m *Message) error {
	if h.r.ProtoMajor == 1 {
		rsp := &http.Response{
			Header:        make(http.Header),
			Body:          ioutil.NopCloser(bytes.NewReader(m.Body)),
			Status:        "500 Internal Server Error",
			StatusCode:    500,
			Proto:         "HTTP/1.1",
			ProtoMajor:    1,
			ProtoMinor:    1,
			ContentLength: int64(len(m.Body)),
		}

		for k, v := range m.Header {
			rsp.Header.Set(k, v)
		}

		return rsp.Write(h.conn)
	}

	return nil
}

func (self *HttpConn) Request() *http.Request {
	return self.r
}

func (self *HttpConn) Response() http.ResponseWriter {
	return self.w
}

func (h *HttpConn) Close() error {
	h.mtx.Lock()
	defer h.mtx.Unlock()
	select {
	case <-h.closed:
		return nil
	default:
		// close the channel
		close(h.closed)

		// close the buffer
		h.r.Body.Close()

		// close the connection
		if h.r.ProtoMajor == 1 {
			return h.conn.Close()
		}
	}

	return nil
}
