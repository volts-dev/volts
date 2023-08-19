package client

import (
	"io/ioutil"
	"net/http"
	"net/url"

	"github.com/volts-dev/volts/internal/body"
	"github.com/volts-dev/volts/internal/header"
)

type httpResponse struct {
	response *http.Response
	_header  map[string]string
	body     *body.TBody

	Status     string // e.g. "200 OK"
	StatusCode int    // e.g. 200
}

func (self *httpResponse) Body() *body.TBody {
	// parse response
	b, err := ioutil.ReadAll(self.response.Body)
	if err == nil {
		//return nil //, errors.InternalServerError("http.client", err.Error())
		self.body.Data.Write(b)
	}

	return self.body
}

func (self *httpResponse) Request() *http.Request {
	return self.response.Request
}

func (self *httpResponse) Cookies() []*http.Cookie {
	return self.response.Cookies()
}

func (self *httpResponse) Header() header.Header {
	return header.Header(self.response.Header)
}

func (self *httpResponse) Location() (*url.URL, error) {
	return self.response.Location()
}

func (r *httpResponse) Read(out interface{}) error {

	return nil
}
