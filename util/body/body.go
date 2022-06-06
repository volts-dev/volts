package body

import (
	"bytes"
	"errors"
	"io"

	"github.com/volts-dev/dataset"
	"github.com/volts-dev/volts/codec"
)

type (
	// NOTED 不提供IO Reader/Writer/Closer
	TBody struct {
		//Buf     interface{} // 存储未编码的数据读取晴空
		Data  *bytes.Buffer // 存储已经编码的数据
		Codec codec.ICodec
		len   int
	}
)

func (self *TBody) Close() error {
	self.Data.Reset()
	return nil
}

func New(coder codec.ICodec, data ...interface{}) *TBody {
	if coder == nil {
		coder = codec.IdentifyCodec(codec.Bytes)
	}

	bd := &TBody{
		Data:  &bytes.Buffer{},
		Codec: coder,
	}

	if data != nil {
		bd.Encode(data[0])
	}

	return bd
}

// 读取原始数据或者编码数据
func (self *TBody) _Read(p []byte) (int, error) {
	return self.Data.Read(p)
}

// as ioreader
func (self *TBody) __Read(p []byte) (int, error) {
	if self.Codec != nil {
		cnt, err := self.Data.Read(p)
		if err != nil && err != io.EOF {
			return 0, err
		}

		raw := make([]byte, 0, len(p)*2) // 必须保留有解码后长度增加的空间
		err = self.Codec.Decode(p[0:cnt], &raw)
		if err != nil {
			return 0, err
		}

		n := copy(p, raw[0:])
		if cnt < len(p) {
			return n, io.EOF
		}
		return n, nil
	}

	return self.Data.Read(p)
}

// 写入原始数据或者编码数据
func (self *TBody) _Write(p []byte) (n int, err error) {
	return self.Data.Write(p)
}

// 写入原始数据转编码数据
func (self *TBody) __Write(p []byte) (n int, err error) {
	if self.Codec != nil {
		data, err := self.Codec.Encode(p)
		if err != nil {
			return 0, err
		}

		return self.Data.Write(data)
	}

	return self.Data.Write(p)
}

// mapping to data type
func (self *TBody) Decode(out interface{}) (err error) {
	if self.Codec != nil {
		return self.Codec.Decode(self.Data.Bytes(), out)
	}

	return errors.New("body not support convert to this type")
}

func (self *TBody) Encode(in interface{}) (int, error) {
	if self.Codec != nil {
		data, err := self.Codec.Encode(in)
		if err != nil {
			return 0, err
		}

		return self.Data.Write(data)
	}
	return 0, errors.New("body not support convert to this type")
}

// the boady data in bytes
func (self *TBody) AsBytes() (res []byte) {
	if self.Codec != nil {
		err := self.Codec.Decode(self.Data.Bytes(), &res)
		if err != nil {
			// NOTED it should not happen!
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
