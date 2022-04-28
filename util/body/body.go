package body

import (
	"bytes"
	"encoding/json"

	"github.com/volts-dev/logger"
	"github.com/volts-dev/volts/codec"
)

type (
	TBody struct {
		Data  bytes.Buffer
		Codec codec.ICodec
	}
)

func (self *TBody) Read(out interface{}) (err error) {
	return self.Codec.Decode(self.Data.Bytes(), out)
}

func (self *TBody) Write(in interface{}) error {
	data, err := self.Codec.Encode(in)
	if err != nil {
		return err
	}
	_, err = self.Data.Write(data)
	return err
}

func (self *TBody) AsBytes() []byte {
	return self.Data.Bytes()
}

// Body 必须是Json结构才能你转
func (self *TBody) AsMap() (map[string]interface{}, error) {
	result := make(map[string]interface{})
	err := json.Unmarshal(self.Data.Bytes(), &result)
	if err != nil {
		logger.Err(err.Error())
		return nil, err
	}

	return result, err
}
