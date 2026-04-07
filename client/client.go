package client

import (
	"github.com/volts-dev/volts/internal/body"
	"github.com/volts-dev/volts/internal/header"
)

type (
	// Client is the interface used to make requests to services.
	// It supports Request/Response via Transport and Publishing via the Broker.
	// Note: NewRequest is intentionally excluded — RpcClient and HttpClient have
	// incompatible NewRequest signatures and should be used directly.
	IClient interface {
		Init(...Option) error
		Config() *Config
		// Call makes a synchronous call and returns the response
		Call(request IRequest, opts ...CallOption) (IResponse, error)
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
		Header() header.Header
		// The unencoded request body
		Body() *body.TBody
		// Write to the encoded request writer. This is nil before a call is made
		//Codec() codec.Writer
		// indicates whether the request will be a streaming one rather than unary
		Stream() bool
		Options() RequestOptions
	}

	// Response is the response received from a service
	IResponse interface {
		Body() *body.TBody
		Header() header.Header
		// StatusCode returns the HTTP/RPC status code of the response
		StatusCode() int
	}
)

// Creates a new request using the default client. Content Type will
// be set to the default within options and use the appropriate codec
//func NewRequest(service, endpoint string, request interface{}, reqOpts ...RequestOption) IRequest {
//	return DefaultClient.NewRequest(service, endpoint, request, reqOpts...)
//}

// Makes a synchronous call to a service using the default client
///func Call(ctx context.Context, request IRequest, opts ...CallOption) (IResponse, error) {
///	return defaultClient.Call(ctx, request, opts...)
///}

// NewClient returns a new client
func New(opts ...Option) IClient {
	return NewRpcClient(opts...) //
}

func Default(opts ...Option) IClient {
	if defaultClient == nil {
		defaultClient = NewRpcClient(opts...)
	} else {
		defaultClient.Init(opts...)
	}
	return defaultClient
}
