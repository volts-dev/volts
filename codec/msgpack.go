package codec

import (
	"github.com/vmihailenco/msgpack/v5"
)

// FIXME 引用包存在不稳定转换
// 该包某些情况下会崩溃

// msgpackCodec uses messagepack marshaler and unmarshaler.
type msgpackCodec struct{}

var MsgPack SerializeType = RegisterCodec("MsgPack", &msgpackCodec{})

// Encode encodes an object into slice of bytes.
func (c msgpackCodec) Encode(i interface{}) ([]byte, error) {
	return msgpack.Marshal(i)
}

// Decode decodes an object from slice of bytes.
func (c msgpackCodec) Decode(data []byte, i interface{}) error {
	return msgpack.Unmarshal(data, i)
}

func (c msgpackCodec) String() string {
	return "MsgPack"
}
