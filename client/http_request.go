package client

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/volts-dev/volts/codec"
)

type httpRequest struct {
	Header      http.Header
	service     string
	method      string
	contentType string
	body        interface{}
	opts        RequestOptions
}

/*
	@service: 目标URL地址
	@method: GET/POST...
*/
func NewHttpRequest(service, method string, data interface{}, opts ...RequestOption) (*httpRequest, error) {
	// 检测Method是否合法字符
	if len(method) > 0 && strings.IndexFunc(method, isNotToken) != -1 {
		return nil, fmt.Errorf("net/http: invalid method %q", method)
	}

	var reqOpts RequestOptions
	for _, o := range opts {
		o(&reqOpts)
	}

	req := &httpRequest{
		Header:      make(http.Header),
		service:     service,
		method:      strings.ToUpper(method),
		body:        data,
		opts:        reqOpts,
		contentType: codec.Bytes.String(),
	}

	if len(reqOpts.ContentType) > 0 {
		req.contentType = reqOpts.ContentType
	}

	return req, nil
}

func (r *httpRequest) AddCookie(c *http.Cookie) {

}

func (h *httpRequest) ContentType() string {
	return h.contentType
}

func (h *httpRequest) Service() string {
	return h.service
}

func (h *httpRequest) Method() string {
	return h.method
}

func (h *httpRequest) Endpoint() string {
	return h.method
}

func (h *httpRequest) __Codec() codec.ICodec {
	return nil
}

func (h *httpRequest) Body() interface{} {
	return h.body
}

func (h *httpRequest) Stream() bool {
	return h.opts.Stream
}
