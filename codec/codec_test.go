package codec

import (
	"testing"
)

func TestFn(t *testing.T) {
	t.Log(JSON)

	codec := IdentifyCodec(JSON)
	t.Log(codec.String())
}
