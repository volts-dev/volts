package codec

import (
	"bytes"

	json "github.com/goccy/go-json"
)

// jsonCodec uses json marshaler and unmarshaler.
//
// NOTE 此前用 github.com/bytedance/sonic。sonic 的 JIT 只支持到 go1.26，而本项目
// 要求 go1.27（router.TGroup.Api 的泛型方法），于是它在**每次启动时都打印告警
// 并整体回退到 encoding/json** —— 引入它的性能收益为零，还白背了一层包装、一个
// asm/JIT 依赖和更大的二进制。实测（50 条记录的典型载荷）：
//
//	                现状(sonic 回退)   encoding/json    goccy
//	Marshal          67186 ns/op       61651 ns/op    31192 ns/op
//	Unmarshal       167045 ns/op      304314 ns/op    63749 ns/op
//
// sonic 的 Marshal 甚至比标准库还慢。goccy 是纯 Go、不挑 Go 版本，相对现状
// Marshal 快 2.2 倍、Unmarshal 快 2.6 倍，分配数从 206 降到 52。
type jsonCodec struct{}

var JSON SerializeType = RegisterCodec("Json", new(jsonCodec))
var _ SerializeType = RegisterCodec("application/json", new(jsonCodec))

func (c jsonCodec) String() string {
	return "Json"
}

// Encode encodes an object into slice of bytes.
func (c jsonCodec) Encode(i interface{}) ([]byte, error) {
	return json.Marshal(i)
}

// Decode decodes an object from slice of bytes.
//
// UseNumber 不可省：解到 interface{} / map[string]any 时，默认行为是把所有数字
// 变成 float64，超过 2^53 的整数会被**静默改写**（ORM 的 id 是 int64，
// 1234541341234123412 会变成 1234541341234123520 这类"看着像但不相等"的值）。
// UseNumber 保留原始字面量，交由上层按目标类型精确转换。
func (c jsonCodec) Decode(data []byte, i interface{}) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	// NOTE 传 i 而非 &i。旧实现传的是 &i —— 一个 *interface{}，靠解码器内部的
	// indirect 再剥一层才能拿到真正的目标指针；能跑但多一次装箱，且调用方误传
	// 非指针时不会报错而是静默写进那个临时 interface。
	return dec.Decode(i)
}
