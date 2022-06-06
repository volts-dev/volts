package client

import (
	"fmt"
	"io"
	"net/http"
	_url "net/url"
	"strings"

	"github.com/volts-dev/volts/util/body"
)

type httpRequest struct {
	URL    *_url.URL
	Header http.Header
	url    string
	method string
	//contentType   string
	ContentLength int64
	body          *body.TBody
	opts          RequestOptions
}

/*
	@service: 目标URL地址
	@method: GET/POST...
	@data: 原始数据
*/
func newHttpRequest(method, url string, data interface{}, opts ...RequestOption) (*httpRequest, error) {
	// 检测Method是否合法字符
	if len(method) > 0 && strings.IndexFunc(method, isNotToken) != -1 {
		return nil, fmt.Errorf("net/http: invalid method %q", method)
	}

	u, err := _url.Parse(url)
	if err != nil {
		return nil, err
	}

	reqOpts := RequestOptions{}
	for _, o := range opts {
		o(&reqOpts)
	}

	// 初始化Body数据编码器
	/*	cf, err := newHTTPCodec(reqOpts.ContentType)
		if err != nil {
			// Warn
			logger.Warnf("%s,Will using defalut codec %s for this request!", err.Error(), codec.Bytes.String())
			//return nil, errors.InternalServerError("http.client", )
		}*/

	req := &httpRequest{
		URL:    u,
		Header: make(http.Header),
		body:   body.New(reqOpts.Codec),
		url:    url,
		method: strings.ToUpper(method),
		opts:   reqOpts,
	}

	err = req.write(data)
	if err != nil {
		return nil, err
	}
	return req, nil
}

func (self *httpRequest) write(data interface{}) error {
	if data == nil {
		return nil
	}

	var err error
	switch v := data.(type) {
	case io.Reader:
		d, err := io.ReadAll(v)
		if err != nil {
			return err
		}
		_, err = self.body.Encode(d)
	default:
		_, err = self.body.Encode(v)
	}

	self.ContentLength = int64(self.body.Data.Len())
	return err
}

func (r *httpRequest) AddCookie(c *http.Cookie) {
	s := c.String()
	if c := r.Header.Get("Cookie"); c != "" {
		r.Header.Set("Cookie", c+"; "+s)
	} else {
		r.Header.Set("Cookie", s)
	}
}

func (r *httpRequest) Referer() string {
	return r.Header.Get("Referer")
}

func (self *httpRequest) ContentType() string {
	return self.body.Codec.String()
}

func (h *httpRequest) Service() string {
	return h.URL.Host
}

func (h *httpRequest) Method() string {
	return h.method
}

func (h *httpRequest) Endpoint() string {
	return h.method
}

func (self *httpRequest) Body() *body.TBody {
	return self.body
}

func (h *httpRequest) Stream() bool {
	return h.opts.Stream
}
