package codec

import (
	"testing"
)

type dummyCodec struct{ name string }

func (c dummyCodec) Encode(i interface{}) ([]byte, error)  { return nil, nil }
func (c dummyCodec) Decode(d []byte, i interface{}) error  { return nil }
func (c dummyCodec) String() string                        { return c.name }

// 内置 codec 的 id 必须保持不变 —— 它是**线上协议的一部分**，
// 改动会让新旧版本互相无法解码。
//
// 这些值由 crc32(name) 截断到低 8 位得到（SerializeType 是 byte）。
func TestBuiltinCodecIdsAreStable(t *testing.T) {
	cases := map[string]SerializeType{
		"MsgPack":          244,
		"Gob":              75,
		"Json":             123,
		"application/json": 2,
		"Bytes":            202,
		"ProtoBuffer":      25,
	}

	for name, want := range cases {
		if got := HashName(name); got != want {
			t.Errorf("HashName(%q) = %d, want %d —— id 变化会破坏线上协议兼容性", name, got, want)
		}
	}
}

// Gob 默认不得被对端选中。
//
// 序列化类型是协议头里的一个字节（HTTP 侧是 Content-Type），完全由对端指定；
// 而 encoding/gob 解码不可信输入是已知的内存放大/DoS 面。
func TestGobNotWireSelectableByDefault(t *testing.T) {
	if c := IdentifyCodec(Gob); c == nil {
		t.Fatal("Gob 应当仍然注册着，只是不允许被对端选中")
	}
	if c := IdentifyWireCodec(Gob); c != nil {
		t.Fatal("Gob 不得被对端通过线上字节选中")
	}
}

// 常规 codec 不受影响。
func TestCommonCodecsAreWireSelectable(t *testing.T) {
	for _, st := range []SerializeType{JSON, MsgPack, ProtoBuffer, Bytes} {
		if IdentifyWireCodec(st) == nil {
			t.Errorf("codec %v 应当允许被对端选中", st)
		}
	}
}

// 受限 codec 可以显式放开 —— 内网可信链路上确实要用 Gob 的场景。
func TestSetWireAllowedOptsIn(t *testing.T) {
	t.Cleanup(func() { SetWireAllowed("Gob", false) })

	if err := SetWireAllowed("Gob", true); err != nil {
		t.Fatalf("SetWireAllowed: %v", err)
	}
	if IdentifyWireCodec(Gob) == nil {
		t.Fatal("显式放开后 Gob 应可被选中")
	}
}

func TestSetWireAllowedUnknownName(t *testing.T) {
	if err := SetWireAllowed("no-such-codec", true); err == nil {
		t.Fatal("未注册的名字应返回错误")
	}
}

// 哈希冲突必须在注册期 panic，而不是静默覆盖。
//
// SerializeType 是 byte，id 空间只有 256 个槽。此前冲突时后注册的会直接顶掉
// 先注册的，表现为"某个 codec 莫名其妙变成了另一个"，几乎无法排查。
func TestRegisterCodecPanicsOnHashCollision(t *testing.T) {
	// 构造一个与 "Json"(123) 撞 id 的名字
	var colliding string
	for _, candidate := range []string{
		"zz1", "zz2", "zz3", "collide-a", "collide-b", "collide-c",
	} {
		if HashName(candidate) == HashName("Json") {
			colliding = candidate
			break
		}
	}
	if colliding == "" {
		// 没找到现成的就暴力搜一个
		for i := 0; i < 100000; i++ {
			name := "probe-" + itoa(i)
			if HashName(name) == HashName("Json") {
				colliding = name
				break
			}
		}
	}
	if colliding == "" {
		t.Skip("未能构造出冲突名字")
	}

	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("注册与 Json 冲突的名字 %q 必须 panic，不能静默覆盖", colliding)
		}
	}()
	RegisterCodec(colliding, dummyCodec{name: colliding})
}

// 同名重复注册是幂等的（覆盖实现），不应被冲突检测误伤。
func TestRegisterCodecSameNameIsIdempotent(t *testing.T) {
	name := "test-idempotent"
	first := RegisterCodec(name, dummyCodec{name: "v1"})
	second := RegisterCodec(name, dummyCodec{name: "v2"})

	if first != second {
		t.Fatalf("同名注册得到不同 id: %d vs %d", first, second)
	}
	if got := IdentifyCodec(second).String(); got != "v2" {
		t.Fatalf("重复注册应覆盖实现，得到 %q", got)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
