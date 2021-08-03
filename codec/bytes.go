package codec

import (
	"fmt"
	"reflect"
)

type byteCodec struct{}

var Bytes SerializeType = RegisterCodec("Bytes", &byteCodec{})

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

func (c byteCodec) String() string {
	return "Bytes"
}
