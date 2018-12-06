package client

// Request is the interface for a synchronous request used by Call or Stream
type (
	IRequest interface {
		Service() string
		Method() string
		ContentType() string
		Request() interface{}
		// indicates whether the request will be a streaming one rather than unary
		Stream() bool
	}
)
