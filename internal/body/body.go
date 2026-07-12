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
		Data  *bytes.Buffer // 存储已经编码的数据
		Codec codec.ICodec
		len   int
	}
)

func (self *TBody) Reset() error {
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

func (self *TBody) AsBuffer() *bytes.Buffer {
	return bytes.NewBuffer(self.AsBytes())
}

// the boady data in bytes
//
// 始终直接返回底层 buffer 的原始字节，不经过 Codec.Decode：Data 存的就是
// 从连接读到的原始 body 字节（见 THttpRequest.Body），"AsBytes" 的语义就是
// 拿这份原始字节，与当前 Codec 是什么无关。
//
// 曾经的实现对非 nil Codec 一律走 self.Codec.Decode(self.Data.Bytes(), &res)
// （res 是 []byte）：当请求带 Content-Type: application/json 时，Codec 是
// jsonCodec，等于让 JSON 解码器把一个 JSON object body 解码进 *[]byte 目标——
// 这必然失败（JSON 只对字符串值做 []byte 的 base64 解码，object/array 直接报错），
// 导致这里的 log.Panic(err) 触发，调用方（如 ctx.Body().AsBuffer().Bytes() 后
// 自行 json.Unmarshal 到具体 struct 的常见模式）500。仅 Codec 恰好是 byteCodec
// 时旧实现才等价于直接返回 Data.Bytes()（Decode 里 case *[]byte 透传），故此处
// 改动对该场景行为不变，对其余 Codec 场景是纯粹的修复。
func (self *TBody) AsBytes() []byte {
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
