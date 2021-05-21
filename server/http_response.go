package server

import "net/http"

type (
	httpResponse struct {
		rsp http.ResponseWriter
	}
)

func (self *httpResponse) Interface() interface{} {
	return self.rsp
}
func (self *httpResponse) WriteHeader(map[string]string) {

}

// write a response directly to the client
func (self *httpResponse) Write(data []byte) (int, error) {
	return self.rsp.Write(data)
}

// Inite and Connect a new ResponseWriter when a new request is coming
func (self *httpResponse) Connect(w http.ResponseWriter) {
	self.rsp = w
}
