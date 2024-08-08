package transport

import (
	"io"
	"net/http"
	"strings"

	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/internal/body"
	"github.com/volts-dev/volts/logger"
)

type (
	THttpRequest struct {
		*http.Request
		Transport ITransport
		body      *body.TBody //
		codec     codec.ICodec
	}
)

// 提供给Router的context使用
func NewHttpRequest(req *http.Request) *THttpRequest {
	r := &THttpRequest{
		Request: req,
	}

	r.body = body.New(r.Codec())
	return r
}

func (self *THttpRequest) Codec() codec.ICodec {
	if self.codec == nil {
		ct := self.Request.Header.Get("Content-Type")
		ct = strings.Split(ct, ";")[0] // 分割“application/json;charset=UTF-8”
		// TODO 添加更多
		var st codec.SerializeType
		switch ct {
		case "application/json":
			st = codec.JSON
		default:
			st = codec.Use(ct)
		}
		self.codec = codec.IdentifyCodec(st)
	}

	return self.codec
}

func (self *THttpRequest) Body() *body.TBody {
	if self.body.Data.Len() == 0 {
		data, err := io.ReadAll(self.Request.Body)
		if err != nil {
			logger.Errf("Read request body faild with an error : %s!", err.Error())
		}

		self.body.Data.Write(data)
	}

	return self.body
}

func (self *THttpRequest) Interface() interface{} {
	return self.Request
}

func (self *THttpRequest) ____Method() string {
	return self.Request.Method
}

func (self *THttpRequest) ____ContentType() string {
	return self.Request.Header.Get("Content-Type")
}

// Header of the request
func (self *THttpRequest) Header() http.Header {
	return self.Request.Header
}

// Body is the initial decoded value
// Body() interface{}
// Read the undecoded request body
func (self *THttpRequest) Read() ([]byte, error) { return nil, nil }

// The encoded message stream
// Codec() codec.Reader
// Indicates whether its a stream
func (self *THttpRequest) Stream() bool {
	return false
}
