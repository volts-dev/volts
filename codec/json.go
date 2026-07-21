package codec

import (
	"bytes"
	"encoding/json"
)

// jsonCodec uses json marshaler and unmarshaler.
//
// 使用标准库 encoding/json —— JSON 是框架最核心的编解码路径，这里刻意不引入
// 第三方实现：
//   - 零依赖：不背 asm/JIT、不用 unsafe、不随第三方仓库的维护状况波动
//   - 跟随 Go 版本演进：新版 Go 对 encoding/json 的优化自动生效，
//     不会出现"库只支持到某个 Go 版本"的死结
//
// 代价是知情的。实测（50 条记录典型载荷，gotip/go1.27，两边同口径都走
// Decoder+UseNumber）：
//
//	                     encoding/json      goccy/go-json
//	Encode                61797 ns/op        29689 ns/op
//	Decode(→struct)      140982 ns/op        62877 ns/op
//	Decode(→map[string]any) 263720 ns/op    111280 ns/op
//
// 即标准库约慢 2 倍。这是为"零第三方依赖 + 永远跟得上 Go 版本"付的价 ——
// 若某天 JSON 成为实测瓶颈，替换点集中在本文件的两个函数里，随时可换。
//
// 历史：曾用 github.com/bytedance/sonic —— 它的 JIT 只支持到 go1.26，而本项目
// 要求 go1.27（router.TGroup.Api 的泛型方法），于是每次启动都打印告警并整体
// 回退到 encoding/json：性能收益为零，却白背了一层包装、一个 asm 依赖和更大的
// 二进制。这正是"为了性能引入第三方 JSON 库"最典型的翻车方式。
//
// 备注：gotip 已带 encoding/json/v2（GOEXPERIMENT=jsonv2 时 v1 会架在 v2 上，
// 性能大幅改善）。待它转正即可白拿收益，无需改动本文件。
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
