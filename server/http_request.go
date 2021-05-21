package server

import (
	"net/http"

	"github.com/volts-dev/volts/transport"
)

type (
	httpRequest struct {
		req *http.Request
	}
)

func (self *httpRequest) Interface() interface{} {
	return self.req
}

func (self *httpRequest) Method() string {
	return self.req.Method
}

func (self *httpRequest) ContentType() string {
	return self.req.Header.Get("Content-Type")
}

// Header of the request
func (self *httpRequest) Header() transport.IHeader {
	return self.req.Header
}

// Body is the initial decoded value
//Body() interface{}
// Read the undecoded request body
func (self *httpRequest) Read() ([]byte, error) { return nil, nil }

// The encoded message stream
//Codec() codec.Reader
// Indicates whether its a stream
func (self *httpRequest) Stream() bool {
	return false
}
