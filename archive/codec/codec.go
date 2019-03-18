package codec

import (
	"encoding/json"
	"fmt"
	"reflect"

	proto "github.com/gogo/protobuf/proto"
	pb "github.com/golang/protobuf/proto"
	"github.com/vmihailenco/msgpack"
)

type (
	// Codec defines the interface that decode/encode payload.
	ICodec interface {
		Encode(i interface{}) ([]byte, error)
		Decode(data []byte, i interface{}) error
	}

	// ByteCodec uses raw slice pf bytes and don't encode/decode.
	ByteCodec struct{}
	// SerializeType defines serialization type of payload.
	SerializeType byte
)

const (
	// SerializeNone uses raw []byte and don't serialize/deserialize
	SerializeNone SerializeType = iota
	// JSON for payload.
	JSON
	// ProtoBuffer for payload.
	ProtoBuffer
	// MsgPack for payload
	MsgPack
)

var (
	// Codecs are codecs supported by RPC.
	Codecs = map[SerializeType]ICodec{
		SerializeNone: &ByteCodec{},
		JSON:          &JSONCodec{},
		ProtoBuffer:   &PBCodec{},
		MsgPack:       &MsgpackCodec{},
	}
)

// RegisterCodec register customized codec.
func RegisterCodec(t SerializeType, c ICodec) {
	Codecs[t] = c
}

// Encode returns raw slice of bytes.
func (c ByteCodec) Encode(i interface{}) ([]byte, error) {
	if data, ok := i.([]byte); ok {
		return data, nil
	}

	return nil, fmt.Errorf("%T is not a []byte", i)
}

// Decode returns raw slice of bytes.
func (c ByteCodec) Decode(data []byte, i interface{}) error {
	reflect.ValueOf(i).SetBytes(data)
	return nil
}

// JSONCodec uses json marshaler and unmarshaler.
type JSONCodec struct{}

// Encode encodes an object into slice of bytes.
func (c JSONCodec) Encode(i interface{}) ([]byte, error) {
	return json.Marshal(i)
}

// Decode decodes an object from slice of bytes.
func (c JSONCodec) Decode(data []byte, i interface{}) error {
	return json.Unmarshal(data, i)
}

// PBCodec uses protobuf marshaler and unmarshaler.
type PBCodec struct{}

// Encode encodes an object into slice of bytes.
func (c PBCodec) Encode(i interface{}) ([]byte, error) {
	if m, ok := i.(proto.Marshaler); ok {
		return m.Marshal()
	}

	if m, ok := i.(pb.Message); ok {
		return pb.Marshal(m)
	}

	return nil, fmt.Errorf("%T is not a proto.Marshaler", i)
}

// Decode decodes an object from slice of bytes.
func (c PBCodec) Decode(data []byte, i interface{}) error {
	if m, ok := i.(proto.Unmarshaler); ok {
		return m.Unmarshal(data)
	}

	if m, ok := i.(pb.Message); ok {
		return pb.Unmarshal(data, m)
	}

	return fmt.Errorf("%T is not a proto.Unmarshaler", i)
}

// MsgpackCodec uses messagepack marshaler and unmarshaler.
type MsgpackCodec struct{}

// Encode encodes an object into slice of bytes.
func (c MsgpackCodec) Encode(i interface{}) ([]byte, error) {
	return msgpack.Marshal(i)
}

// Decode decodes an object from slice of bytes.
func (c MsgpackCodec) Decode(data []byte, i interface{}) error {
	return msgpack.Unmarshal(data, i)
}
