package transport

import (
	"io/ioutil"
	"net/http"

	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/logger"
	"github.com/volts-dev/volts/util/body"
)

type (
	THttpRequest struct {
		*http.Request
		Transport ITransport
		body      *body.TBody //
	}
)

func NewHttpRequest(req *http.Request) *THttpRequest {
	ct := req.Header.Get("Content-Type")
	c := codec.IdentifyCodec(codec.CodecByName(ct))

	return &THttpRequest{
		Request: req,
		body:    body.New(c),
	}
}

func (self *THttpRequest) Body() *body.TBody {
	if self.body.Data.Len() == 0 {
		data, err := ioutil.ReadAll(self.Request.Body)
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
func (self *THttpRequest) ___Header() IHeader {
	return self.Request.Header
}

// Body is the initial decoded value
//Body() interface{}
// Read the undecoded request body
func (self *THttpRequest) Read() ([]byte, error) { return nil, nil }

// The encoded message stream
//Codec() codec.Reader
// Indicates whether its a stream
func (self *THttpRequest) Stream() bool {
	return false
}
