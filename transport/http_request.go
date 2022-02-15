package transport

import (
	"net/http"
)

type (
	THttpRequest struct {
		Transport ITransport
		req       *http.Request
	}
)

func (self *THttpRequest) Interface() interface{} {
	return self.req
}

func (self *THttpRequest) Method() string {
	return self.req.Method
}

func (self *THttpRequest) ContentType() string {
	return self.req.Header.Get("Content-Type")
}

// Header of the request
func (self *THttpRequest) Header() IHeader {
	return self.req.Header
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
