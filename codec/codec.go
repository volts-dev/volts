package codec

import (
	"fmt"
	"hash/crc32"
	"reflect"
)

type (
	// Codec defines the interface that decode/encode payload.
	ICodec interface {
		Encode(i interface{}) ([]byte, error)
		Decode(data []byte, i interface{}) error
	}

	// SerializeType defines serialization type of payload.
	SerializeType byte

	// byteCodec uses raw slice pf bytes and don't encode/decode.
	byteCodec struct{}
)

const (

	// SerializeNone uses raw []byte and don't serialize/deserialize
	SerializeNone SerializeType = iota
	/*// JSON for payload.
	JSON
	// ProtoBuffer for payload.
	ProtoBuffer
	// MsgPack for payload
	MsgPack
	*/
)

var (
	// Codecs are codecs supported by rpc.
	Codecs = map[SerializeType]ICodec{
		SerializeNone: &byteCodec{},
	}
)

// RegisterCodec register customized codec.
func RegisterCodec(name string, c ICodec) SerializeType {
	h := hashName(name)
	Codecs[h] = c
	return h
}

func hashName(s string) SerializeType {
	v := int(crc32.ChecksumIEEE([]byte(s)))
	if v >= 0 {
		return SerializeType(v)
	}
	if -v >= 0 {
		return SerializeType(-v)
	}
	// v == MinInt
	return SerializeType(0)
}

// Encode returns raw slice of bytes.
func (c byteCodec) Encode(i interface{}) ([]byte, error) {
	if data, ok := i.([]byte); ok {
		return data, nil
	}

	return nil, fmt.Errorf("%T is not a []byte", i)
}

// Decode returns raw slice of bytes.
func (c byteCodec) Decode(data []byte, i interface{}) error {
	reflect.ValueOf(i).SetBytes(data)
	return nil
}
