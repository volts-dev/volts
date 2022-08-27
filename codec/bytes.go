package codec

import (
	"fmt"
)

type byteCodec struct{}

// Bytes only support []byte and string data type
var Bytes SerializeType = RegisterCodec("Bytes", &byteCodec{})

// Encode returns raw slice of bytes.
func (c byteCodec) Encode(i interface{}) ([]byte, error) {
	if i == nil {
		return []byte{}, nil
	}

	switch v := i.(type) {
	case []byte:
		return v, nil
	case string:
		return []byte(v), nil

	default:
		return nil, fmt.Errorf("%T is not a []byte", i)
	}
}

// Decode returns raw slice of bytes.
func (c byteCodec) Decode(data []byte, out interface{}) error {
	// #1 reflect method but is slow
	//reflect.Indirect(reflect.ValueOf(i)).SetBytes(data)

	// #2
	switch v := out.(type) {
	case *[]byte:
		*v = *&data
		return nil
	case *string:
		*v = string(data)
		return nil

	default:
		return fmt.Errorf("%T is not a []byte", out)
	}
}

func (c byteCodec) String() string {
	return "Bytes"
}
