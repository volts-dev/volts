package codec

import (
	"encoding/json"
	"log"
	"testing"
	"time"
	//"github.com/araddon/dateparse"
)

// 大整数必须无损往返。
//
// 这是 JSON 编解码在本框架里最要命的一个点：ORM 的 id 是 int64，而 JSON 数字
// 一旦被当成 float64 处理，超过 2^53 的部分就会被静默改写 —— 1234541341234123412
// 会变成 1234541341234123520 这类"看着像但不相等"的值，且不报任何错。
//
// NOTE 原测试名为 Int64Value 却根本没断言 int64，反而把 "2014-04-26" 解进
// time.Time：那个字符串不是 RFC3339，encoding/json 必然报错，测试恒定失败。
// 现在断言它真正该管的事。
func TestJosnInt64Value(t *testing.T) {
	type payload struct {
		Id   int64
		Name string
	}

	const bigId int64 = 1234541341234123412 // > 2^53，float64 存不下

	coder := new(jsonCodec)
	buf, err := coder.Encode(payload{Id: bigId, Name: "adc"})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	var got payload
	if err := coder.Decode(buf, &got); err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if got.Id != bigId {
		t.Fatalf("int64 精度丢失: got %d, want %d（差 %d）", got.Id, bigId, got.Id-bigId)
	}
	if got.Name != "adc" {
		t.Fatalf("Name = %q, want %q", got.Name, "adc")
	}
}

// 时间字段按 RFC3339 往返。
//
// 非 RFC3339 的日期串（如 "2014-04-26"）解进 time.Time 会报错，这是
// encoding/json 的既定行为，需要宽松解析请在业务侧用自定义类型。
func TestJosnTimeValue(t *testing.T) {
	type payload struct {
		Time time.Time
	}

	want := time.Date(2014, 4, 26, 10, 30, 0, 0, time.UTC)

	coder := new(jsonCodec)
	buf, err := coder.Encode(payload{Time: want})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	var got payload
	if err := coder.Decode(buf, &got); err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if !got.Time.Equal(want) {
		t.Fatalf("Time = %v, want %v", got.Time, want)
	}
}

func TestJosnStringValue(t *testing.T) {
	type Test1 struct {
		Id   int64
		Name string
		Time any
	}

	coder := new(jsonCodec)
	buf, err := coder.Encode(Test1{Id: 1234541341234123412, Name: "adc", Time: "2014-04-26"}) //"2016-01-02"
	if err != nil {

	}

	var test Test1
	err = coder.Decode(buf, &test)
	if err != nil {
		t.Fatal(err)
	}
	log.Print(test)
}

// 解到 interface{} 时必须保留数字字面量（UseNumber），不得退化成 float64。
//
// 这是本次从 sonic 换到 goccy 时唯一有风险的语义点：默认行为会把所有 JSON 数字
// 变成 float64，超过 2^53 的整数被**静默改写** —— ORM 的 id 是 int64，
// 1234541341234123412 会变成 1234541341234123520 这类"看着像但不相等"的值，
// 且不报任何错。call_kw 这类走 map[string]any 的路径全都要靠它。
func TestJsonDecodeUsesNumberForInterface(t *testing.T) {
	const raw = `{"id":1234541341234123412,"name":"x"}`

	coder := new(jsonCodec)

	var got map[string]any
	if err := coder.Decode([]byte(raw), &got); err != nil {
		t.Fatalf("Decode: %v", err)
	}

	num, ok := got["id"].(json.Number)
	if !ok {
		t.Fatalf("id 的类型是 %T，want json.Number —— UseNumber 没生效，大整数会被静默改写", got["id"])
	}

	v, err := num.Int64()
	if err != nil {
		t.Fatalf("Int64: %v", err)
	}
	if v != 1234541341234123412 {
		t.Fatalf("id = %d, want 1234541341234123412（差 %d）", v, v-1234541341234123412)
	}
}

// 嵌套结构里的大整数同样不能丢精度。
func TestJsonDecodeNestedBigInt(t *testing.T) {
	const raw = `{"rows":[{"id":9007199254740993}]}`

	var got map[string]any
	if err := new(jsonCodec).Decode([]byte(raw), &got); err != nil {
		t.Fatalf("Decode: %v", err)
	}

	rows := got["rows"].([]any)
	row := rows[0].(map[string]any)

	num, ok := row["id"].(json.Number)
	if !ok {
		t.Fatalf("嵌套 id 类型 = %T, want json.Number", row["id"])
	}
	if v, _ := num.Int64(); v != 9007199254740993 {
		t.Fatalf("嵌套 id = %d, want 9007199254740993", v)
	}
}

// 解到具体结构体时行为不变。
func TestJsonDecodeIntoStruct(t *testing.T) {
	type row struct {
		Id   int64  `json:"id"`
		Name string `json:"name"`
	}

	var got row
	if err := new(jsonCodec).Decode([]byte(`{"id":1234541341234123412,"name":"x"}`), &got); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.Id != 1234541341234123412 || got.Name != "x" {
		t.Fatalf("got = %+v", got)
	}
}
