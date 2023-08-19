package codec

import (
	"bytes"
	"encoding/json"

	"github.com/hzmsrv/sonic"
	"github.com/hzmsrv/sonic/decoder"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/bsonrw"
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
	return sonic.Marshal(i)
	//return json.Marshal(i)
	//return bson.Marshal(i)
}

func (c jsonCodec) _Decode(data []byte, i interface{}) error {
	dc, err := bson.NewDecoder(bsonrw.NewBSONDocumentReader(data))
	if err != nil {
		return err
	}
	dc.UseLocalTimeZone()
	return dc.Decode(i)
}

// Decode decodes an object from slice of bytes.
func (c jsonCodec) Decode(data []byte, i interface{}) error {
	// FIXME TODO 时间转换错误
	dc := decoder.NewStreamDecoder(bytes.NewReader(data))
	dc.UseInt64()
	return dc.Decode(&i)
}

func (c jsonCodec) __Decode(data []byte, i interface{}) error {
	dc := json.NewDecoder(bytes.NewBuffer(data))
	dc.UseNumber()
	return dc.Decode(&i)
}
