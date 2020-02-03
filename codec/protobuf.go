package codec

import (
	"fmt"

	proto "github.com/gogo/protobuf/proto"
	pb "github.com/golang/protobuf/proto"
)

// pbCodec uses protobuf marshaler and unmarshaler.
type pbCodec struct{}

var ProtoBuffer SerializeType

func init() {
	ProtoBuffer = RegisterCodec("ProtoBuffer", &pbCodec{})
}

// Encode encodes an object into slice of bytes.
func (c pbCodec) Encode(i interface{}) ([]byte, error) {
	if m, ok := i.(proto.Marshaler); ok {
		return m.Marshal()
	}

	if m, ok := i.(pb.Message); ok {
		return pb.Marshal(m)
	}

	return nil, fmt.Errorf("%T is not a proto.Marshaler", i)
}

// Decode decodes an object from slice of bytes.
func (c pbCodec) Decode(data []byte, i interface{}) error {
	if m, ok := i.(proto.Unmarshaler); ok {
		return m.Unmarshal(data)
	}

	if m, ok := i.(pb.Message); ok {
		return pb.Unmarshal(data, m)
	}

	return fmt.Errorf("%T is not a proto.Unmarshaler", i)
}
