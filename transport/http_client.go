package transport

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/volts-dev/volts/util/buf"
)

type (
	httpTransportClient struct {
		transport *HttpTransport
		addr      string
		conn      net.Conn
		config    DialConfig
		once      sync.Once

		sync.RWMutex

		// request must be stored for response processing
		r    chan *http.Request
		bl   []*http.Request
		buff *bufio.Reader

		// local/remote ip
		local  string
		remote string
	}
)

func (t *httpTransportClient) Transport() ITransport {
	return t.transport
}

func (t *httpTransportClient) Conn() net.Conn {
	return t.conn
}

func (h *httpTransportClient) Local() string {
	return h.local
}

func (h *httpTransportClient) Remote() string {
	return h.remote
}

func (h *httpTransportClient) Send(m *Message) error {
	header := make(http.Header)

	for k, v := range m.Header {
		header.Set(k, v)
	}

	b := buf.New(bytes.NewBuffer(m.Body))
	defer b.Close()

	req := &http.Request{
		Method: "POST",
		URL: &url.URL{
			Scheme: "http",
			Host:   h.addr,
		},
		Header:        header,
		Body:          b,
		ContentLength: int64(b.Len()),
		Host:          h.addr,
	}

	h.Lock()
	h.bl = append(h.bl, req)
	select {
	case h.r <- h.bl[0]:
		h.bl = h.bl[1:]
	default:
	}
	h.Unlock()

	// set timeout if its greater than 0
	if h.transport.config.WriteTimeout > time.Duration(0) {
		h.conn.SetDeadline(time.Now().Add(h.transport.config.WriteTimeout))
	}

	return req.Write(h.conn)
}

func (h *httpTransportClient) Recv(m *Message) error {
	if m == nil {
		return errors.New("message passed in is nil")
	}

	var r *http.Request
	if !h.config.Stream {
		rc, ok := <-h.r
		if !ok {
			return io.EOF
		}
		r = rc
	}

	// set timeout if its greater than 0
	if h.transport.config.ReadTimeout > time.Duration(0) {
		h.conn.SetDeadline(time.Now().Add(h.transport.config.ReadTimeout))
	}

	rsp, err := http.ReadResponse(h.buff, r)
	if err != nil {
		return err
	}
	defer rsp.Body.Close()

	b, err := ioutil.ReadAll(rsp.Body)
	if err != nil {
		return err
	}

	if rsp.StatusCode != 200 {
		return errors.New(rsp.Status + ": " + string(b))
	}

	m.Body = b

	if m.Header == nil {
		m.Header = make(map[string]string, len(rsp.Header))
	}

	for k, v := range rsp.Header {
		if len(v) > 0 {
			m.Header[k] = v[0]
		} else {
			m.Header[k] = ""
		}
	}

	return nil
}

func (h *httpTransportClient) Close() error {
	h.once.Do(func() {
		h.Lock()
		h.buff.Reset(nil)
		h.Unlock()
		close(h.r)
	})
	return h.conn.Close()
}
