package body

import (
	"bytes"
	"errors"

	"github.com/volts-dev/dataset"
	"github.com/volts-dev/volts/codec"
)

type (
	TBody struct {
		Data  bytes.Buffer
		Codec codec.ICodec
	}
)

func (self *TBody) Close() error {
	return nil
}

// as ioreader
func (self *TBody) Read(p []byte) (int, error) {
	if self.Codec != nil {
		err := self.Codec.Decode(self.Data.Bytes(), p)
		if err != nil {
			return 0, err
		}
		return len(p), nil
	}

	return self.Data.Read(p)
}

// mapping to data type
func (self *TBody) ReadData(out interface{}) (err error) {
	if self.Codec != nil {
		return self.Codec.Decode(self.Data.Bytes(), out)
	}

	return errors.New("body not support convert to this type")
}

func (self *TBody) Write(p []byte) (n int, err error) {
	if self.Codec != nil {
		data, err := self.Codec.Encode(p)
		if err != nil {
			return 0, err
		}
		return self.Data.Write(data)
	}
	return self.Data.Write(p)
}

func (self *TBody) WriteData(in interface{}) error {
	if self.Codec != nil {
		data, err := self.Codec.Encode(in)
		if err != nil {
			return err
		}
		_, err = self.Data.Write(data)
	}
	return errors.New("body not support convert to this type")
}

// the boady data in bytes
func (self *TBody) AsBytes() (res []byte) {
	if self.Codec != nil {
		err := self.Codec.Decode(self.Data.Bytes(), res)
		if err != nil {
			return nil
		}
		return res
	}

	return self.Data.Bytes()
}

// TODO 优化
// 根据序列强制转换为Map
func (self *TBody) AsMap(c ...codec.SerializeType) (map[string]interface{}, error) {
	if c != nil {
		self.Codec = codec.IdentifyCodec(c[0])
	}

	if self.Codec != nil {
		result := make(map[string]interface{})
		err := self.Codec.Decode(self.Data.Bytes(), &result)
		if err != nil {
			return nil, err
		}

		return result, err
	}

	return nil, errors.New("Must input the serialize type")
}

// 根据序列强制转换为TRecordSet
// 默认为codec.JSON
func (self *TBody) AsRecordset(c ...codec.SerializeType) (rec *dataset.TRecordSet, err error) {
	if c != nil {
		self.Codec = codec.IdentifyCodec(c[0])
	} else if self.Codec == nil {
		self.Codec = codec.IdentifyCodec(codec.JSON)
	}

	m := make(map[string]interface{})
	err = self.Codec.Decode(self.Data.Bytes(), &m)
	if err != nil {
		return nil, err
	}
	return dataset.NewRecordSet(m), err
}
