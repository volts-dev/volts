package codec

import (
	"hash/crc32"
	"strings"
	"sync"
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
// codecsMu 保护 codecs / names 两个全局 map：RegisterCodec 通常在 init 时单线程
// 调用，但任意运行时注册会与并发的 Use / IdentifyCodec / String 形成数据竞态。
var (
	codecsMu sync.RWMutex
	codecs   = make(map[SerializeType]ICodec)
	names    = make(map[string]SerializeType)
)

func (self SerializeType) String() string {
	codecsMu.RLock()
	c, has := codecs[self]
	codecsMu.RUnlock()
	if has {
		return c.String()
	}
	return "Unknown"
}

// RegisterCodec register customized codec.
func RegisterCodec(name string, codec ICodec) SerializeType {
	h := HashName(name)
	codecsMu.Lock()
	codecs[h] = codec
	names[strings.ToLower(name)] = h
	codecsMu.Unlock()
	return h
}

// 提供编码类型
func Use(name string) SerializeType {
	codecsMu.RLock()
	defer codecsMu.RUnlock()
	if v, has := names[strings.ToLower(name)]; has {
		return v
	}
	return 0
}

func IdentifyCodec(st SerializeType) ICodec {
	codecsMu.RLock()
	defer codecsMu.RUnlock()
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
