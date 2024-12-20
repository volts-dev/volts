package client

import (
	"io"

	"github.com/volts-dev/volts/internal/body"
	"github.com/volts-dev/volts/internal/header"
)

type (
	rpcRequest struct {
		service       string //服务名
		method        string //method和endpoint目前是一样的是方法名
		ContentLength int64
		header        header.Header
		body          *body.TBody
		opts          RequestOptions
	}
)

func newRpcRequest(service, method string, data interface{}, opts ...RequestOption) (*rpcRequest, error) {
	reqOpts := RequestOptions{}
	for _, o := range opts {
		o(&reqOpts)
	}

	req := &rpcRequest{
		service:       service,
		method:        method,
		body:          body.New(reqOpts.Codec),
		ContentLength: 0,
		opts:          reqOpts,
	}

	err := req.write(data)
	if err != nil {
		return nil, err
	}
	return req, nil
}

// TODO
// 写入请求二进制数据
func (self *rpcRequest) write(data interface{}) error {
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

	case []byte:
		if self.opts.Encoded {
			_, err = self.body.Data.Write(v)
		} else {
			_, err = self.body.Encode(v)
		}
	default:
		_, err = self.body.Encode(v)
	}

	self.ContentLength = int64(self.body.Data.Len())
	return err
}

func (r *rpcRequest) Service() string {
	return r.service
}

func (self *rpcRequest) Method() string {
	return self.method
}

func (self *rpcRequest) ContentType() string {
	return self.body.Codec.String()
}

func (self *rpcRequest) Body() *body.TBody {
	return self.body
}

func (*rpcRequest) Stream() bool {
	return false
}
func (self *rpcRequest) Header() header.Header {
	if self.header == nil {
		self.header = make(header.Header)
	}
	return self.header
}

func (self *rpcRequest) Options() RequestOptions {
	return self.opts
}
