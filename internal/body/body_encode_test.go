package body

import (
	"bytes"
	"strings"
	"testing"

	"github.com/volts-dev/volts/codec"
)

func TestEncodeJSONKeepsBodyAndAppendSemantics(t *testing.T) {
	for _, typ := range []codec.SerializeType{codec.JSON, codec.Use("application/json")} {
		body := New(codec.IdentifyCodec(typ))
		data := body.Data
		first, err := body.Encode(map[string]string{"name": "first"})
		if err != nil || first != len(`{"name":"first"}`) {
			t.Fatalf("first encode: %d, %v", first, err)
		}
		if body.Data != data {
			t.Fatal("Data pointer changed")
		}
		second, err := body.Encode(map[string]string{"name": "second"})
		if err != nil || second != len(`{"name":"second"}`) {
			t.Fatalf("second encode: %d, %v", second, err)
		}
		if got, want := body.Data.String(), `{"name":"first"}{"name":"second"}`; got != want {
			t.Fatalf("body = %s, want %s", got, want)
		}
		if err := body.Reset(); err != nil {
			t.Fatal(err)
		}
		if _, err := body.Encode("reset"); err != nil || body.Data.String() != `"reset"` {
			t.Fatalf("encode after reset: %q, %v", body.Data.String(), err)
		}
	}
}

func TestEncodeBytesRetainsCallerOwnership(t *testing.T) {
	original := []byte("hello")
	body := New(codec.IdentifyCodec(codec.Bytes))
	if _, err := body.Encode(original); err != nil {
		t.Fatal(err)
	}
	original[0] = 'x'
	if got := body.Data.String(); got != "hello" {
		t.Fatalf("body aliased caller's slice: %q", got)
	}
}

type borrowedJSONCodec struct{ data []byte }

func (c *borrowedJSONCodec) String() string             { return "Json" }
func (c *borrowedJSONCodec) Encode(any) ([]byte, error) { return c.data, nil }
func (c *borrowedJSONCodec) Decode([]byte, any) error   { return nil }

func TestEncodeCustomCodecCannotBorrowEvenIfNamedJSON(t *testing.T) {
	coder := &borrowedJSONCodec{data: []byte("borrowed")}
	body := New(coder)
	if _, err := body.Encode(nil); err != nil {
		t.Fatal(err)
	}
	coder.data[0] = 'x'
	if got := body.Data.String(); got != "borrowed" {
		t.Fatalf("body aliased custom codec's bytes: %q", got)
	}
}

func TestEncodeJSONErrorDoesNotChangeBody(t *testing.T) {
	body := New(codec.IdentifyCodec(codec.JSON))
	body.Data.WriteString("previous")
	if _, err := body.Encode(make(chan int)); err == nil || body.Data.String() != "previous" {
		t.Fatalf("failed encode changed body: %q, %v", body.Data.String(), err)
	}
}

func BenchmarkEncodeJSONBody(b *testing.B) {
	payload := map[string]string{"data": strings.Repeat("x", 16<<10)}
	coder := codec.IdentifyCodec(codec.JSON)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		body := New(coder)
		if _, err := body.Encode(payload); err != nil {
			b.Fatal(err)
		}
		if body.Data.Len() != 16395 || !bytes.HasPrefix(body.Data.Bytes(), []byte(`{"data":`)) {
			b.Fatal("bad payload")
		}
	}
}
