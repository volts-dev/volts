package codec

import (
	"bytes"
	"encoding/gob"
)

type gobCodec struct{}

// Gob 默认**不允许被对端选中**。
//
// 序列化类型由协议头里的一个字节（或 HTTP 的 Content-Type）指定，完全受对端
// 控制。而 encoding/gob 解码不可信输入是已知的内存放大/DoS 面：构造的类型
// 描述符可以触发巨量分配。本端要显式用 Gob（如 client 配置 SerializeType）
// 不受影响；确需让对端也能选中，调用：
//
//	codec.SetWireAllowed("Gob", true)
var Gob SerializeType = RegisterRestrictedCodec("Gob", newGobCodec())

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
