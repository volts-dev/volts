package codec

import (
	"github.com/vmihailenco/msgpack"
)

// msgpackCodec uses messagepack marshaler and unmarshaler.
type msgpackCodec struct{}

var MsgPack SerializeType

func init() {
	MsgPack = RegisterCodec("MsgPack", &msgpackCodec{})
}

// Encode encodes an object into slice of bytes.
func (c msgpackCodec) Encode(i interface{}) ([]byte, error) {
	return msgpack.Marshal(i)
}

// Decode decodes an object from slice of bytes.
func (c msgpackCodec) Decode(data []byte, i interface{}) error {
	return msgpack.Unmarshal(data, i)
}
