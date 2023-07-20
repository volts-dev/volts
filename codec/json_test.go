package codec

import "testing"

func TestJosnInt64Value(t *testing.T) {
	var value int64 = 1674612877677301760
	type Test struct {
		Id   int64
		Name string
	}
	coder := new(jsonCodec)
	buf, err := coder.Encode(&Test{Id: value, Name: "TEST"})
	if err != nil {

	}
	var test map[string]any
	coder.Decode(buf, &test)
	v, ok := test["Id"].(int64)
	if !ok {
		t.Fatal()
	}

	if v != value {
		t.Fatal()
	}
}
