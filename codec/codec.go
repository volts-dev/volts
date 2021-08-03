package codec

import (
	"hash/crc32"
)

type (
	// SerializeType defines serialization type of payload.
	SerializeType byte

	// Codec defines the interface that decode/encode payload.
	ICodec interface {
		Encode(input interface{}) ([]byte, error)
		Decode(input []byte, output interface{}) error
		//Close() error
		String() string
	}
)

// Codecs are codecs supported by rpc.
var codecs = make(map[SerializeType]ICodec)

// RegisterCodec register customized codec.
func RegisterCodec(name string, codec ICodec) SerializeType {
	h := hashName(name)
	codecs[h] = codec
	return h
}

func IdentifyCodec(st SerializeType) ICodec {
	return codecs[st]
}

func hashName(s string) SerializeType {
	v := int(crc32.ChecksumIEEE([]byte(s))) // 输入一个字符等到唯一标识
	if v >= 0 {
		return SerializeType(v)
	}
	if -v >= 0 {
		return SerializeType(-v)
	}
	// v == MinInt
	return SerializeType(0)
}
