package client

import "github.com/volts-dev/volts/util/body"

type httpResponse struct {
	header map[string]string
	body   *body.TBody
}

func (r *httpResponse) Body() *body.TBody {
	return r.body
}
func (r *httpResponse) Header() map[string]string {
	return r.header
}
func (r *httpResponse) Read(out interface{}) error {

	return nil
}
