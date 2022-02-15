package client

import "github.com/volts-dev/volts/codec"

type (
	rpcRequest struct {
		service     string //
		method      string
		endpoint    string
		contentType codec.SerializeType // body 内容传输格式
		body        interface{}
		opts        RequestOptions
	}
)

func NewRpcRequest(service, endpoint string, content interface{}, reqOpts ...RequestOption) *rpcRequest {
	var opts RequestOptions

	for _, o := range reqOpts {
		o(&opts)
	}

	contentType := codec.Bytes
	// set the content-type specified
	if len(opts.ContentType) > 0 {
		contentType = codec.CodecByName(opts.ContentType)
	}

	return &rpcRequest{
		service:     service,
		method:      endpoint,
		endpoint:    endpoint,
		body:        content,
		contentType: contentType,
		opts:        opts,
	}
}

// TODO
// 写入请求二进制数据
func (r *rpcRequest) Write(data interface{}) {
	r.body = data
}

func (r *rpcRequest) Service() string {
	return r.service
}

func (self *rpcRequest) Method() string {
	return self.method
}

func (self *rpcRequest) ContentType() string {
	return self.contentType.String()
}

func (self *rpcRequest) SetContentType(name codec.SerializeType) {
	self.contentType = name
}

func (self *rpcRequest) Body() interface{} {
	return self.body
}

func (*rpcRequest) Stream() bool {
	return false
}
