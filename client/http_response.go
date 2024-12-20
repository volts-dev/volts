package client

import (
	"io"
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

// TODO 修改到结构里
type HttpResponse = httpResponse

func (self *httpResponse) Body() *body.TBody {
	if self.body.Data.Len() == 0 {
		b, err := io.ReadAll(self.response.Body)
		if err == nil {
			//return nil //, errors.InternalServerError("http.client", err.Error())
			log.Err(err)
		}
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
