package client

import (
	"github.com/volts-dev/volts/codec"
)

type httpRequest struct {
	service     string
	method      string
	contentType string
	request     interface{}
	opts        RequestOptions
}

func NewHttpRequest(service, method string, request interface{}, reqOpts ...RequestOption) *httpRequest {
	var opts RequestOptions
	for _, o := range reqOpts {
		o(&opts)
	}

	req := &httpRequest{
		service: service,
		method:  method,
		request: request,
		opts:    opts,
	}

	if len(opts.ContentType) == 0 {
		req.contentType = opts.ContentType
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

func (h *httpRequest) Codec() codec.ICodec {
	return nil
}

func (h *httpRequest) Body() interface{} {
	return h.request
}

func (h *httpRequest) Stream() bool {
	return h.opts.Stream
}
