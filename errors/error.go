package errors

import (
	"errors"
	"fmt"
	"net/http"
)

// ErrShutdown connection is closed.
var (
	ErrShutdown         = errors.New("connection is shut down")
	ErrUnsupportedCodec = errors.New("unsupported codec")
)

type (
	Error struct {
		Id     string `protobuf:"bytes,1,opt,name=id,proto3" json:"id,omitempty"`
		Code   int32  `protobuf:"varint,2,opt,name=code,proto3" json:"code,omitempty"`
		Detail string `protobuf:"bytes,3,opt,name=detail,proto3" json:"detail,omitempty"`
		Status string `protobuf:"bytes,4,opt,name=status,proto3" json:"status,omitempty"`
	}
)

func (self *Error) Error() string {
	return self.Detail
}

// InternalServerError generates a 500 error.
func InternalServerError(id, format string, a ...interface{}) error {
	return &Error{
		Id:     id,
		Code:   500,
		Detail: fmt.Sprintf(format, a...),
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

// Timeout generates a 408 error.
func Timeout(id, format string, a ...interface{}) error {
	return &Error{
		Id:     id,
		Code:   408,
		Detail: fmt.Sprintf(format, a...),
		Status: http.StatusText(408),
	}
}
