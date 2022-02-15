package client

import (
	"github.com/volts-dev/volts/codec"
)

type httpRequest struct {
	service     string
	method      string
	contentType string
	body        interface{}
	opts        RequestOptions
}

func NewHttpRequest(service, method string, data interface{}, opts ...RequestOption) *httpRequest {
	var reqOpts RequestOptions
	for _, o := range opts {
		o(&reqOpts)
	}

	req := &httpRequest{
		service:     service,
		method:      method,
		body:        data,
		opts:        reqOpts,
		contentType: codec.Bytes.String(),
	}

	if len(reqOpts.ContentType) > 0 {
		req.contentType = reqOpts.ContentType
	}

	return req
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
