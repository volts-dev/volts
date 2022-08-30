package codec

import (
	"testing"
)

func TestByted(t *testing.T) {
	t.Log(JSON)
	str := []byte("你好！")
	codec := IdentifyCodec(Bytes)
	out := make([]byte, 0)
	err := codec.Decode(str, &out)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(out))
}

func TestFn(t *testing.T) {
	t.Log(JSON)

	codec := IdentifyCodec(JSON)
	t.Log(codec.String())
}
