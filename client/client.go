package client

import (
	"context"
	"time"

	"github.com/volts-dev/volts/util/body"
)

type (
	// Client is the interface used to make requests to services.
	// It supports Request/Response via Transport and Publishing via the Broker.
	// It also supports bidirectional streaming of requests.
	IClient interface {
		Init(...Option) error
		Config() *Config
		////NewMessage(topic string, msg interface{}, opts ...MessageOption) Message
		///NewRequest(service, endpoint string, req interface{}, reqOpts ...RequestOption) Request
		//Call(ctx context.Context, req Request, rsp interface{} ) error
		Call(ctx context.Context, request IRequest, opts ...CallOption) (IResponse, error)
		//Stream(ctx context.Context, req Request, opts ...CallOption) (Stream, error)
		//Publish(ctx context.Context, msg Message, opts ...PublishOption) error
		//String() string
		//NewRequest(service, method string, request interface{}, reqOpts ...RequestOption) IRequest
	}

	// Request is the interface for a synchronous request used by Call or Stream
	IRequest interface {
		// The service to call
		Service() string
		// The action to take
		Method() string
		// The endpoint to invoke
		//Endpoint() string
		// The content type
		ContentType() string
		// The unencoded request body
		Body() interface{}
		// Write to the encoded request writer. This is nil before a call is made
		//Codec() codec.Writer
		// indicates whether the request will be a streaming one rather than unary
		Stream() bool
	}

	// Response is the response received from a service
	IResponse interface {
		Body() *body.TBody
		// Read the response
		//Codec() codec.Reader
		// read the header
		Header() map[string]string
		// Read the undecoded response
		//Read(out interface{}) error
	}
)

var (
	// Default Client
	defaultClient IClient = NewRpcClient()
	// DefaultRetry is the default check-for-retry function for retries
	DefaultRetry = RetryOnError
	// DefaultRetries is the default number of times a request is tried
	DefaultRetries = 1
	// DefaultRequestTimeout is the default request timeout
	DefaultRequestTimeout = time.Second * 50 // TODO
	// DefaultPoolSize sets the connection pool size
	DefaultPoolSize = 100
	// DefaultPoolTTL sets the connection pool ttl
	DefaultPoolTTL = time.Minute
)

// Creates a new request using the default client. Content Type will
// be set to the default within options and use the appropriate codec
//func NewRequest(service, endpoint string, request interface{}, reqOpts ...RequestOption) IRequest {
//	return DefaultClient.NewRequest(service, endpoint, request, reqOpts...)
//}

// Makes a synchronous call to a service using the default client
func Call(ctx context.Context, request IRequest, opts ...CallOption) (IResponse, error) {
	return defaultClient.Call(ctx, request, opts...)
}

// NewClient returns a new client
func New(opts ...Option) IClient {
	return NewRpcClient(opts...)
}

func Default(opts ...Option) IClient {
	defaultClient.Init(opts...)
	return defaultClient
}
