package codec

import (
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/protoadapt"
)

// pbCodec uses protobuf marshaler and unmarshaler.
//
// NOTE 此前依赖 github.com/gogo/protobuf —— 该仓库 2022 年起已归档、不再维护，
// 且历史上出过可由畸形输入触发无限递归 panic 的漏洞（GO-2021-0053）。
// 一个直接解析线上不可信字节的 codec，不该压在停止维护的库上。
//
// 现改用官方 google.golang.org/protobuf，并保留对老式消息的兼容：
//   - v2 消息（protoreflect）：直接 proto.Marshal
//   - v1 消息（golang/protobuf 或 gogo 生成）：经 protoadapt 适配后走 v2
//   - 自带 Marshal/Unmarshal 方法的类型（gogo marshaler 插件的产物）：
//     用本地声明的接口识别，从而在不依赖 gogo 的前提下保留这条快路径
type pbCodec struct{}

// protoMarshaler / protoUnmarshaler 与 gogo 的同名接口结构一致。
// 在本地声明即可继续识别既有生成代码，同时不引入对 gogo 的依赖。
type (
	protoMarshaler interface {
		Marshal() ([]byte, error)
	}
	protoUnmarshaler interface {
		Unmarshal([]byte) error
	}
)

var ProtoBuffer SerializeType = RegisterCodec("ProtoBuffer", &pbCodec{})

// Encode encodes an object into slice of bytes.
func (c pbCodec) Encode(i interface{}) ([]byte, error) {
	// 生成代码自带的快路径优先（与旧实现的取舍顺序保持一致）
	if m, ok := i.(protoMarshaler); ok {
		return m.Marshal()
	}

	if m, ok := i.(proto.Message); ok {
		return proto.Marshal(m)
	}

	// 老式（v1）消息：golang/protobuf 与 gogo 生成的都走这里
	if m, ok := i.(protoadapt.MessageV1); ok {
		return proto.Marshal(protoadapt.MessageV2Of(m))
	}

	return nil, fmt.Errorf("%T is not a protobuf message", i)
}

// Decode decodes an object from slice of bytes.
func (c pbCodec) Decode(data []byte, i interface{}) error {
	if m, ok := i.(protoUnmarshaler); ok {
		return m.Unmarshal(data)
	}

	if m, ok := i.(proto.Message); ok {
		return proto.Unmarshal(data, m)
	}

	if m, ok := i.(protoadapt.MessageV1); ok {
		return proto.Unmarshal(data, protoadapt.MessageV2Of(m))
	}

	return fmt.Errorf("%T is not a protobuf message", i)
}

func (c pbCodec) String() string {
	return "ProtoBuffer"
}
