package client

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/volts-dev/volts/codec"
	"github.com/volts-dev/volts/internal/pool"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/transport"
)

type borrowedResponseConn struct {
	cancellationBase
	payload []byte
}

func (*borrowedResponseConn) Send(*transport.Message) error { return nil }
func (c *borrowedResponseConn) Recv(m *transport.Message) error {
	m.SetSerializeType(codec.JSON)
	m.Header["trace"] = "custom-response"
	m.Payload = c.payload
	return nil
}

func TestRPCCustomTransportResponseOwnsPayload(t *testing.T) {
	conn := &borrowedResponseConn{payload: []byte(`{"value":"original"}`)}
	cli := NewRpcClient(WithSerializeType(codec.JSON))
	cli.pool = &cancellationPool{
		get: func() (pool.Conn, error) { return conn, nil },
		release: func(pool.Conn, error) error {
			// Reuse borrowed storage as soon as the connection is returned.
			copy(conn.payload, bytes.Repeat([]byte("X"), len(conn.payload)))
			return nil
		},
	}
	req, err := cli.NewRequest("test", "/payload", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rsp, err := cli.call(ctx, &registry.Node{Address: "unused"}, req, CallOptions{RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := rsp.Body().Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["value"] != "original" || rsp.Header().Get("trace") != "custom-response" {
		t.Fatalf("corrupt response: %v", got)
	}
}
