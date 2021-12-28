package client

type (
	rpcRequest struct {
		service     string
		method      string
		endpoint    string
		contentType string
		body        interface{}
		opts        RequestOptions
	}
)

func NewRpcRequest(service, endpoint string, request interface{}, contentType string, reqOpts ...RequestOption) *rpcRequest {
	var opts RequestOptions

	for _, o := range reqOpts {
		o(&opts)
	}

	// set the content-type specified
	if len(opts.ContentType) > 0 {
		contentType = opts.ContentType
	}

	return &rpcRequest{
		service:     service,
		method:      endpoint,
		endpoint:    endpoint,
		body:        request,
		contentType: contentType,
		opts:        opts,
	}
}

func (r *rpcRequest) Service() string {
	return r.service
}

func (self *rpcRequest) Method() string {
	return self.method
}

func (self *rpcRequest) ContentType() string {
	return self.contentType
}

func (self *rpcRequest) Body() interface{} {
	return self.body
}

func (*rpcRequest) Stream() bool {
	return false
}
