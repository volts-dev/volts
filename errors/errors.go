package errors

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// ErrShutdown connection is closed.
var (
	Shutdown          = New("", 0, "connection is shut down")
	PermissionDenied  = New("", 500, "permission denied.")
	UnavailableService = New("", 500, "Unavailable Service")
)

type (
	Error struct {
		Id     string `protobuf:"bytes,1,opt,name=id,proto3" json:"id,omitempty"`
		Code   int    `protobuf:"varint,2,opt,name=code,proto3" json:"code,omitempty"`
		Detail string `protobuf:"bytes,3,opt,name=detail,proto3" json:"detail,omitempty"`
		Status string `protobuf:"bytes,4,opt,name=status,proto3" json:"status,omitempty"`
	}
)

func (self Error) Error() string {
	return self.Detail
}

// New generates a custom error.
func New(id string, code int, detail string) error {
	return &Error{
		Id:     id,
		Code:   code,
		Detail: detail,
		Status: http.StatusText(int(code)),
	}
}

// Parse tries to parse a JSON string into an error. If that
// fails, it will set the given string as the error detail.
func Parse(err string) *Error {
	e := new(Error)
	jsonErr := json.Unmarshal([]byte(err), e)
	if jsonErr != nil {
		e.Detail = err
	}
	return e
}

// BadRequest generates a 400 error.
func BadRequest(id, msg string) error {
	return &Error{
		Id:     id,
		Code:   400,
		Detail: msg,
		Status: http.StatusText(400),
	}
}

// Unauthorized generates a 401 error.
func Unauthorized(id string, msg ...any) error {
	return &Error{
		Id:     id,
		Code:   401,
		Detail: fmt.Sprintf("%s Session Expired %v", id, msg),
		Status: http.StatusText(401),
	}
}

// Forbidden generates a 403 error.
func Forbidden(id string, msg ...any) error {
	return &Error{
		Id:     id,
		Code:   403,
		Detail: fmt.Sprintf("%s Forbidden %v", id, msg),
		Status: http.StatusText(403),
	}
}

// NotFound generates a 404 error.
func NotFound(id, msg string) error {
	return &Error{
		Id:     id,
		Code:   404,
		Detail: msg,
		Status: http.StatusText(404),
	}
}

// MethodNotAllowed generates a 405 error.
func MethodNotAllowed(id, msg string) error {
	return &Error{
		Id:     id,
		Code:   405,
		Detail: msg,
		Status: http.StatusText(405),
	}
}

// Timeout generates a 408 error.
func Timeout(id, msg string) error {
	return &Error{
		Id:     id,
		Code:   408,
		Detail: msg,
		Status: http.StatusText(408),
	}
}

// Conflict generates a 409 error.
func Conflict(id, msg string) error {
	return &Error{
		Id:     id,
		Code:   409,
		Detail: msg,
		Status: http.StatusText(409),
	}
}

// InternalServerError generates a 500 error.
func InternalServerError(id string, msg ...any) error {
	return &Error{
		Id:     id,
		Code:   500,
		Detail: fmt.Sprintf("%s Internal Server Error %v", id, msg),
		Status: http.StatusText(500),
	}
}

func UnsupportedCodec(id string, a ...interface{}) error {
	return &Error{
		Id:     id,
		Code:   500,
		Detail: fmt.Sprintf("%s unsupported codec %v", id, a),
		Status: http.StatusText(500),
	}
}
