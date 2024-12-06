package codec

import (
	"bytes"

	"github.com/bytedance/sonic"
	"github.com/bytedance/sonic/decoder"
	//"github.com/hzmsrv/sonic"
	//"github.com/hzmsrv/sonic/decoder"
)

// jsonCodec uses json marshaler and unmarshaler.
type jsonCodec struct{}

var JSON SerializeType = RegisterCodec("Json", new(jsonCodec))
var _ SerializeType = RegisterCodec("application/json", new(jsonCodec))

func (c jsonCodec) String() string {
	return "Json"
}

// Encode encodes an object into slice of bytes.
func (c jsonCodec) Encode(i interface{}) ([]byte, error) {
	r, _ := sonic.MarshalString(i)

	return []byte(r), nil
	//return json.Marshal(i)
	//return bson.Marshal(i)
}

// Decode decodes an object from slice of bytes.
func (c jsonCodec) Decode(data []byte, i interface{}) error {
	// FIXME TODO 时间转换错误
	dc := decoder.NewStreamDecoder(bytes.NewReader(data))
	dc.UseNumber()
	dc.UseInt64()
	return dc.Decode(&i)
}
