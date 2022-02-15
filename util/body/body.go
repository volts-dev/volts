package body

import (
	"bytes"

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
