package codec

import (
	"bytes"
	"encoding/gob"
)

type gobCodec struct{}

var Gob SerializeType = RegisterCodec("Gob", newGobCodec())

func newGobCodec() *gobCodec {
	return &gobCodec{}
}

// Encode encodes an object into a slice of bytes using gob encoding.
func (c gobCodec) Encode(i interface{}) ([]byte, error) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(i); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Decode decodes an object from a slice of bytes using gob encoding.
func (c gobCodec) Decode(data []byte, i interface{}) error {
	return gob.NewDecoder(bytes.NewReader(data)).Decode(i)
}

func (c gobCodec) String() string {
	return "Gob"
}
