package body

import (
	"testing"

	"github.com/volts-dev/volts/codec"
)

type (
	Args struct {
		Num1 int
		Num2 int
		Str  string
		Flt  float64
	}
)

func TestEncoding(t *testing.T) {
	body := &TBody{
		Codec: codec.IdentifyCodec(codec.MsgPack),
	}
	arg := &Args{
		Num1: 1,
		Num2: 2,
		Flt:  0.123,
	}
	body.Write(arg)
	arg2 := &Args{}
	body.Read(&arg2)

	t.Log(arg2)
}