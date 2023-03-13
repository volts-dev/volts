package codec

import (
	"hash/crc32"
	"strings"
)

type (
	// SerializeType defines serialization type of payload.
	SerializeType byte

	// Codec defines the interface that decode/encode payload.
	ICodec interface {
		Encode(interface{}) ([]byte, error)
		Decode([]byte, interface{}) error
		String() string
	}
)

// Codecs are codecs supported by rpc.
var codecs = make(map[SerializeType]ICodec)
var names = make(map[string]SerializeType)

func (self SerializeType) String() string {
	if c, has := codecs[self]; has {
		return c.String()
	}

	return "Unknown"
}

// RegisterCodec register customized codec.
func RegisterCodec(name string, codec ICodec) SerializeType {
	h := HashName(name)
	codecs[h] = codec
	names[strings.ToLower(name)] = h
	return h
}

// 提供编码类型
func Use(name string) SerializeType {
	if v, has := names[strings.ToLower(name)]; has {
		return v
	}
	return 0
}

func IdentifyCodec(st SerializeType) ICodec {
	return codecs[st]
}

func HashName(s string) SerializeType {
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
