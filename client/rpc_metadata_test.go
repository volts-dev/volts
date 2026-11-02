package client

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/volts-dev/volts/internal/metadata"
	"github.com/volts-dev/volts/internal/pool"
	"github.com/volts-dev/volts/registry"
	"github.com/volts-dev/volts/transport"
)

type metadataPool struct {
	pool.Pool
	conn pool.Conn
}

func (p *metadataPool) Get(string, ...transport.DialOption) (pool.Conn, error) { return p.conn, nil }
func (p *metadataPool) Release(pool.Conn, error) error                         { return nil }

type metadataBase = pool.Conn
type metadataConn struct {
	metadataBase
	headers chan map[string]string
}

func (c *metadataConn) Send(m *transport.Message) error {
	copied := make(map[string]string, len(m.Header))
	for k, v := range m.Header {
		copied[k] = v
	}
	c.headers <- copied
	return errors.New("captured outgoing frame")
}

func TestRPCRequestHeadersOverrideInheritedMetadata(t *testing.T) {
	for _, value := range []string{"obo-for-next-service", ""} {
		t.Run(value, func(t *testing.T) {
			inherited := metadata.Metadata{"On-Behalf-Of": "obo-for-previous-service", "on-behalf-of": "lowercase-old-credential", "Trace-Id": "keep-this-trace"}
			ctx, cancel := context.WithTimeout(metadata.NewContext(context.Background(), inherited), time.Second)
			defer cancel()
			cli := NewRpcClient()
			captured := make(chan map[string]string, 1)
			cli.pool = &metadataPool{conn: &metadataConn{headers: captured}}
			req, err := cli.NewRequest("next-service", "/model.fields", nil)
			if err != nil {
				t.Fatal(err)
			}
			// Empty is intentional: a hook can remove a hop-specific inherited credential.
			req.Header().Set("On-Behalf-Of", value)
			if _, err = cli.call(ctx, &registry.Node{Address: "unused"}, req, CallOptions{RequestTimeout: time.Second}); err == nil {
				t.Fatal("capture connection unexpectedly succeeded")
			}
			select {
			case got := <-captured:
				if got["On-Behalf-Of"] != value {
					t.Fatalf("inbound credential replaced this hop's header: %q", got["On-Behalf-Of"])
				}
				if _, inherited := got["on-behalf-of"]; inherited {
					t.Fatal("case-variant inbound credential survived alongside the replacement")
				}
				if got["Trace-Id"] != "keep-this-trace" {
					t.Fatal("trace metadata was lost")
				}
			case <-time.After(time.Second):
				t.Fatal("no outgoing frame")
			}
			if inherited["On-Behalf-Of"] != "obo-for-previous-service" {
				t.Fatal("caller context was mutated")
			}
		})
	}
}
