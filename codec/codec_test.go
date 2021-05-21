package codec

import (
	"testing"
)

func TestFn(t *testing.T) {
	t.Log(JSON)
	t.Log(MsgPack)

	codec := IdentifyCodec(JSON)
	t.Log(codec.String())
}
