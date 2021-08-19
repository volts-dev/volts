package transport

import (
	"testing"

	"github.com/volts-dev/volts/codec"
)

func TestA(t *testing.T) {
	msg := newMessage()
	msg.SetSerializeType(codec.JSON)
	t.Log(codec.JSON, msg.SerializeType())
}
