package codec

import (
	"testing"

	"google.golang.org/protobuf/types/known/wrapperspb"
)

// v2（protoreflect）消息必须能无损往返。
// 用 wrapperspb 作样本：它是官方 protobuf 库自带的标准类型，无需生成代码。
func TestProtobufRoundTripV2(t *testing.T) {
	c := pbCodec{}

	in := wrapperspb.String("hello 你好")
	data, err := c.Encode(in)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	out := &wrapperspb.StringValue{}
	if err := c.Decode(data, out); err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if out.GetValue() != in.GetValue() {
		t.Fatalf("往返后 = %q, want %q", out.GetValue(), in.GetValue())
	}
}

// 数值类型同样验证一遍，确保走的不是某个字符串特例。
func TestProtobufRoundTripInt64(t *testing.T) {
	c := pbCodec{}

	const want int64 = 1234541341234123412 // > 2^53

	data, err := c.Encode(wrapperspb.Int64(want))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	out := &wrapperspb.Int64Value{}
	if err := c.Decode(data, out); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if out.GetValue() != want {
		t.Fatalf("往返后 = %d, want %d", out.GetValue(), want)
	}
}

// 自带 Marshal/Unmarshal 的类型走快路径 —— 这条兼容的是 gogo marshaler 插件
// 生成的代码。退掉 gogo 依赖后靠本地声明的接口识别，能力不能丢。
type fastPathMessage struct {
	payload         []byte
	marshalCalled   bool
	unmarshalCalled bool
}

func (m *fastPathMessage) Marshal() ([]byte, error) {
	m.marshalCalled = true
	return m.payload, nil
}

func (m *fastPathMessage) Unmarshal(b []byte) error {
	m.unmarshalCalled = true
	m.payload = b
	return nil
}

func TestProtobufUsesSelfMarshalFastPath(t *testing.T) {
	c := pbCodec{}

	in := &fastPathMessage{payload: []byte("raw-bytes")}
	data, err := c.Encode(in)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !in.marshalCalled {
		t.Error("未走自带 Marshal 的快路径")
	}
	if string(data) != "raw-bytes" {
		t.Errorf("Encode = %q, want %q", data, "raw-bytes")
	}

	out := &fastPathMessage{}
	if err := c.Decode(data, out); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !out.unmarshalCalled {
		t.Error("未走自带 Unmarshal 的快路径")
	}
}

// 非 protobuf 类型必须报错而不是 panic。
func TestProtobufRejectsNonMessage(t *testing.T) {
	c := pbCodec{}

	if _, err := c.Encode(struct{ A int }{1}); err == nil {
		t.Error("Encode 非 protobuf 类型应报错")
	}
	var target struct{ A int }
	if err := c.Decode([]byte("x"), &target); err == nil {
		t.Error("Decode 到非 protobuf 类型应报错")
	}
}

// 畸形字节不得 panic —— protobuf 载荷同样来自线上不可信输入。
func TestProtobufRejectsGarbage(t *testing.T) {
	c := pbCodec{}

	garbage := [][]byte{
		{0xFF, 0xFF, 0xFF, 0xFF},
		{0x08},             // 有 tag 无 value
		{0x0A, 0x7F, 0x01}, // 声明长度 127 实际只有 1 字节
	}

	for _, data := range garbage {
		out := &wrapperspb.StringValue{}
		// 报错是预期结果，panic 不是
		_ = c.Decode(data, out)
	}
}
