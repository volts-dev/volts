package client

import (
	"fmt"
	"io"
	"net/http"
	_url "net/url"
	"strings"

	"github.com/volts-dev/volts/internal/body"
	"github.com/volts-dev/volts/internal/header"
)

type httpRequest struct {
	URL    *_url.URL
	header header.Header
	url    string
	method string
	//contentType   string
	ContentLength int64
	body          *body.TBody
	opts          RequestOptions
}

// TODO 修改到结构里
type HttpRequest = httpRequest

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
		header: make(header.Header), // TODO 不初始化
		body:   body.New(reqOpts.Codec),
		url:    url,
		method: strings.ToUpper(method),
		opts:   reqOpts,
	}
	// 检测是否已经重复实现
	req.header.Set("user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/111.0.0.0 Safari/537.36") //
	req.header.Set("Accept", "*/*")                                                                                                                 // TODO
	req.header.Set("Host", u.Host)
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
	if c := r.header.Get("Cookie"); c != "" {
		r.header.Set("Cookie", c+"; "+s)
	} else {
		r.header.Set("Cookie", s)
	}
}

func (r *httpRequest) Referer() string {
	return r.header.Get("Referer")
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

func (self *httpRequest) Header() header.Header {
	if self.header == nil {
		self.header = make(header.Header)
	}
	return self.header
}

func (self *httpRequest) Options() RequestOptions {
	return self.opts
}
