package codec

import (
	"encoding/json"
)

// jsonCodec uses json marshaler and unmarshaler.
type jsonCodec struct{}

var JSON SerializeType

func init() {
	JSON = RegisterCodec("JSON", &jsonCodec{})
}

// Encode encodes an object into slice of bytes.
func (c jsonCodec) Encode(i interface{}) ([]byte, error) {
	return json.Marshal(i)
}

// Decode decodes an object from slice of bytes.
func (c jsonCodec) Decode(data []byte, i interface{}) error {
	return json.Unmarshal(data, i)
}

func (c jsonCodec) String() string {
	return "JSON"
}
