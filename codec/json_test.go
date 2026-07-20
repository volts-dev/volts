package codec

import (
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
