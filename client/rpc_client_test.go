package client

import (
	"context"
	"testing"
	"time"

	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/internal/body"
	"github.com/volts-dev/volts/internal/header"
)

func WithPoolSize(size int) Option {
	return func(cfg *Config) {
		cfg.PoolSize = size
	}
}

func WithPoolTtl(ttl time.Duration) Option {
	return func(cfg *Config) {
		cfg.PoolTtl = ttl
	}
}

func TestRpcClient_ConfigAndInit(t *testing.T) {
	c := NewRpcClient()

	if c.Config() == nil {
		t.Fatal("Expected config not to be nil")
	}

	// Default serialization should be JSON
	if c.Config().Serialize != codec.JSON {
		t.Errorf("Expected default Serialize to be codec.JSON, got %v", c.Config().Serialize)
	}

	err := c.Init(WithPoolSize(10), WithPoolTtl(10*time.Second))
	if err != nil {
		t.Fatalf("Expected nil err on Init, got %v", err)
	}

	if c.Config().PoolSize != 10 {
		t.Errorf("Expected PoolSize 10, got %d", c.Config().PoolSize)
	}
	if c.Config().PoolTtl != 10*time.Second {
		t.Errorf("Expected PoolTtl 10s, got %v", c.Config().PoolTtl)
	}
}

func TestRpcClient_NewRequest(t *testing.T) {
	c := NewRpcClient(WithSerializeType(codec.JSON))

	payload := map[string]string{"msg": "hello"}
	// WithCodec for RequestOption
	req, err := c.NewRequest("TestService", "Test.Method", payload, WithCodec(codec.JSON))
	if err != nil {
		t.Fatalf("Expected no error creating new request, got %v", err)
	}

	if req.Service() != "TestService" {
		t.Errorf("Expected TestService, got %s", req.Service())
	}
	if req.Method() != "Test.Method" {
		t.Errorf("Expected Test.Method, got %s", req.Method())
	}
	
	if req.Body() == nil || req.Body().Data == nil || req.Body().Data.Len() == 0 {
		t.Errorf("Expected encoded body to exist")
	}
}

type mockRequest struct {
	service string
	method  string
	body    *body.TBody
	header  header.Header
}

func (m *mockRequest) Service() string         { return m.service }
func (m *mockRequest) Method() string          { return m.method }
func (m *mockRequest) ContentType() string     { return "application/json" }
func (m *mockRequest) Header() header.Header   { return m.header }
func (m *mockRequest) Body() *body.TBody       { return m.body }
func (m *mockRequest) Stream() bool            { return false }
func (m *mockRequest) Options() RequestOptions { return RequestOptions{} }

func TestRpcClient_next_Proxy(t *testing.T) {
	c := NewRpcClient()

	req := &mockRequest{
		service: "TestService",
	}

	// Specify address to trigger Proxy resolution
	opts := CallOptions{
		Address: []string{"127.0.0.1:8080", "127.0.0.1:8081"},
	}

	nextNode, err := c.next(req, opts)
	if err != nil {
		t.Fatalf("Expected no error from next(), got %v", err)
	}
	if nextNode == nil {
		t.Fatal("Expected next node to be non-nil")
	}

	fmtNode, err := nextNode()
	if err != nil {
		t.Fatalf("Expected no error resolving next node")
	}
	if fmtNode == nil || fmtNode.Address == "" {
		t.Fatalf("Expected valid node address, got %+v", fmtNode)
	}

	found := false
	for _, add := range opts.Address {
		if fmtNode.Address == add {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Resolved proxy node address %v not found in inputs", fmtNode.Address)
	}
}

func TestRpcClient_Call_Timeout(t *testing.T) {
	c := NewRpcClient(WithSerializeType(codec.JSON))

	req := &mockRequest{
		service: "TestService",
		body:    body.New(codec.IdentifyCodec(codec.JSON)),
		header:  make(header.Header),
	}

	opt := WithAddress("127.0.0.1:9099") // use proxy logic to simulate timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	callOpts := WithContext(ctx)

	_, err := c.Call(req, opt, callOpts)
	if err == nil {
		t.Fatal("Expected timeout error, got nil")
	}
}
