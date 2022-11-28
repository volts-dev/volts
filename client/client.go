package client

import (
	"github.com/volts-dev/volts/util/body"
	"github.com/volts-dev/volts/util/header"
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
		//Call(ctx context.Context, request IRequest, opts ...CallOption) (IResponse, error)
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
		Header() header.Header
		// The unencoded request body
		Body() *body.TBody
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
		Header() header.Header
		// Read the undecoded response
		//Read(out interface{}) error
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
func New(opts ...Option) (IClient, error) {
	return NewRpcClient(opts...) //
}

func Default(opts ...Option) IClient {
	if defaultClient == nil {
		var err error
		defaultClient, err = NewRpcClient()
		if err != nil {
			panic(err)
		}
	}
	defaultClient.Init(opts...)
	return defaultClient
}
