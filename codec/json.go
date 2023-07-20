package codec

import (
	"bytes"

	"github.com/bytedance/sonic"
	"github.com/bytedance/sonic/decoder"
)

// jsonCodec uses json marshaler and unmarshaler.
type jsonCodec struct{}

var JSON SerializeType = RegisterCodec("Json", new(jsonCodec))
var _ SerializeType = RegisterCodec("application/json", new(jsonCodec))

func (c jsonCodec) String() string {
	return "Json"
}

// Encode encodes an object into slice of bytes.
func (c jsonCodec) Encode(i interface{}) ([]byte, error) {
	return sonic.Marshal(i)
}

// Decode decodes an object from slice of bytes.
func (c jsonCodec) Decode(data []byte, i interface{}) error {
	dc := decoder.NewStreamDecoder(bytes.NewReader(data))
	dc.UseInt64()
	return dc.Decode(&i)
}
