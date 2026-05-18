package codec

import (
	"fmt"

	"github.com/vmihailenco/msgpack/v5"
)

// msgpackCodec uses messagepack marshaler and unmarshaler.
// 上游 vmihailenco/msgpack 在罕见的反射路径下可能 panic（不规范的反射类型 / 循环引用等）。
// 用 defer recover 把 panic 转成 error，避免单条恶意/畸形负载拖崩整个 server goroutine。
type msgpackCodec struct{}

var MsgPack SerializeType = RegisterCodec("MsgPack", &msgpackCodec{})

// Encode encodes an object into slice of bytes.
func (c msgpackCodec) Encode(i interface{}) (out []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("msgpack encode panicked: %v", r)
		}
	}()
	return msgpack.Marshal(i)
}

// Decode decodes an object from slice of bytes.
func (c msgpackCodec) Decode(data []byte, i interface{}) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("msgpack decode panicked: %v", r)
		}
	}()
	return msgpack.Unmarshal(data, i)
}

func (c msgpackCodec) String() string {
	return "MsgPack"
}
