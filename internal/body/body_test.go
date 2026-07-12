package body

import (
	"fmt"
	"io"
	"testing"

	"github.com/volts-dev/volts/codec"
)

type (
	Args struct {
		Num1 int
		Num2 int
		Str  string
		Flt  float64
		Any  any
	}
)

func TestRW(t *testing.T) {
	body := New(codec.IdentifyCodec(codec.JSON))

	if _, err := body.Data.Write([]byte(`你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！你好！`)); err != nil {
		t.Fatal(err)
	}

	//as := strings.NewReader("你好！")
	//data := make([]byte, 512)

	//body.Read(data)
	out, err := io.ReadAll(body.Data)
	if err != nil {
		t.Fatal(err)
	}

	fmt.Println(string(out))
}

func TestEncoding(t *testing.T) {
	body := New(
		codec.IdentifyCodec(codec.MsgPack),
	)

	str := "hello"
	var val any = &str
	arg := &Args{
		Num1: 1,
		Num2: 2,
		Flt:  0.123,
		Any:  val,
	}
	body.Encode(arg)
	arg2 := &Args{}
	body.Decode(&arg2)

	t.Log(arg2)
}

func TestWithoutEncoding(t *testing.T) {
	// 用 New() 构造(而非裸 &TBody{Codec:...} 字面量),Data 字段才会被初始化为
	// 非 nil 的 *bytes.Buffer——裸字面量下 Data 是 nil,Decode 内部
	// self.Data.Bytes() 对 nil *bytes.Buffer 解引用会 panic,是测试自身的构造
	// 缺陷,与 body 逻辑无关。
	body := New(codec.IdentifyCodec(codec.Bytes))
	arg := &Args{
		Num1: 1,
		Num2: 2,
		Flt:  0.123,
	}
	body.Encode(arg)
	arg2 := &Args{}
	body.Decode(&arg2)

	t.Log(arg2)
}

// TestAsBytesJSONBody 覆盖 AsBytes 的核心场景:Content-Type: application/json
// 的请求体(顶层是 JSON object)。曾经的实现对非 nil Codec 一律走
// self.Codec.Decode(self.Data.Bytes(), &res)(res 是 []byte)——JSON 解码器把
// 一个 object 解码进 *[]byte 目标必然失败,导致 log.Panic。这是 website
// /website/add 等 POST JSON 端点线上 500 的根因(handler panic recovered)。
// AsBytes 现在应始终返回底层原始字节,不管 Codec 是什么。
func TestAsBytesJSONBody(t *testing.T) {
	raw := `{"name":"About Us","add_menu":true}`
	body := New(codec.IdentifyCodec(codec.JSON))
	if _, err := body.Data.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("AsBytes panicked on a JSON object body: %v", r)
		}
	}()

	got := body.AsBytes()
	if string(got) != raw {
		t.Errorf("AsBytes() = %q, want %q", got, raw)
	}
}
