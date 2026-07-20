package codec

import (
	"fmt"
	"hash/crc32"
	"strings"
	"sync"
)

type (
	// SerializeType defines serialization type of payload.
	SerializeType byte

	// Codec defines the interface that decode/encode payload.
	ICodec interface {
		Encode(interface{}) ([]byte, error)
		Decode([]byte, interface{}) error
		String() string
	}
)

// Codecs are codecs supported by rpc.
// codecsMu 保护下列全局 map：RegisterCodec 通常在 init 时单线程调用，
// 但任意运行时注册会与并发的 Use / IdentifyCodec / String 形成数据竞态。
var (
	codecsMu sync.RWMutex
	codecs   = make(map[SerializeType]ICodec)
	names    = make(map[string]SerializeType)
	// ids 反查 SerializeType 对应的注册名，用于检测哈希冲突。
	ids = make(map[SerializeType]string)
	// wireAllowed 标记哪些 codec 允许由对端通过线上字节/Content-Type 选中。
	wireAllowed = make(map[SerializeType]bool)
)

func (self SerializeType) String() string {
	codecsMu.RLock()
	c, has := codecs[self]
	codecsMu.RUnlock()
	if has {
		return c.String()
	}
	return "Unknown"
}

// RegisterCodec register customized codec.
//
// 注册的 codec 允许被对端选中（见 IdentifyWireCodec）。若该 codec 解码不可信
// 输入存在风险，请改用 RegisterRestrictedCodec。
func RegisterCodec(name string, codec ICodec) SerializeType {
	return registerCodec(name, codec, true)
}

// RegisterRestrictedCodec 注册一个**不允许由对端选中**的 codec。
//
// 它照常可以被本端显式引用（如 client 配置 WithSerializeType），但服务端不会
// 因为对端在协议头里填了它的 id 就用它来解码 —— 见 IdentifyWireCodec。
func RegisterRestrictedCodec(name string, codec ICodec) SerializeType {
	return registerCodec(name, codec, false)
}

func registerCodec(name string, codec ICodec, wire bool) SerializeType {
	h := HashName(name)
	lower := strings.ToLower(name)

	codecsMu.Lock()
	defer codecsMu.Unlock()

	// 哈希冲突检测。
	//
	// SerializeType 是 byte，而 HashName 取的是 crc32 的低 8 位 —— id 空间只有
	// 256 个槽。此前冲突时后者**静默覆盖**前者，表现为"某个 codec 莫名其妙变成
	// 了另一个"，极难排查。宁可在启动期炸掉。
	if prev, taken := ids[h]; taken && prev != lower {
		panic(fmt.Sprintf(
			"codec: 注册名 %q 与已注册的 %q 哈希冲突（同为 id %d）。"+
				"SerializeType 是 byte，id 空间仅 256 个槽，请换一个名字。",
			name, prev, h))
	}

	codecs[h] = codec
	names[lower] = h
	ids[h] = lower
	wireAllowed[h] = wire

	return h
}

// SetWireAllowed 调整某个已注册 codec 是否允许被对端选中。
//
// 用于放开默认受限的 codec（如内网可信链路上确实要用 Gob），或反过来收紧
// 某个第三方 codec。name 未注册时返回错误。
func SetWireAllowed(name string, allowed bool) error {
	lower := strings.ToLower(name)

	codecsMu.Lock()
	defer codecsMu.Unlock()

	st, has := names[lower]
	if !has {
		return fmt.Errorf("codec: %q 未注册", name)
	}
	wireAllowed[st] = allowed
	return nil
}

// 提供编码类型
func Use(name string) SerializeType {
	codecsMu.RLock()
	defer codecsMu.RUnlock()
	if v, has := names[strings.ToLower(name)]; has {
		return v
	}
	return 0
}

// IdentifyCodec 按 id 查找 codec，不做任何准入判断。
// 仅用于本端已确定的编码场景；解析对端输入请用 IdentifyWireCodec。
func IdentifyCodec(st SerializeType) ICodec {
	codecsMu.RLock()
	defer codecsMu.RUnlock()
	return codecs[st]
}

// IdentifyWireCodec 按 id 查找 codec，且要求该 codec 允许被对端选中。
//
// 存在的理由：序列化类型是**协议头里的一个字节**（HTTP 侧则是 Content-Type），
// 完全由对端指定。也就是说任何能连上端口的人都能挑选服务端用哪个解码器去解析
// 他发来的字节。encoding/gob 解码不可信输入是已知的内存放大/DoS 面（构造的
// 类型描述符可触发巨量分配），因此 Gob 默认不可被选中。
//
// 返回 nil 表示"未注册"或"不允许被选中"，调用方一律按拒绝处理。
func IdentifyWireCodec(st SerializeType) ICodec {
	codecsMu.RLock()
	defer codecsMu.RUnlock()

	if !wireAllowed[st] {
		return nil
	}
	return codecs[st]
}

// HashName 由注册名派生 codec id。
//
// NOTE 返回值是 SerializeType(byte)，crc32 的高 24 位会被直接截断 ——
// 实际可用 id 只有 256 个。冲突由 registerCodec 检测并 panic。
func HashName(s string) SerializeType {
	return SerializeType(crc32.ChecksumIEEE([]byte(s)))
}
