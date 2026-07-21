package transport

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// FuzzMessageDecode 用任意字节喂 Message.Decode。
//
// 这是整个框架里唯一直接解析**不可信线上字节**的地方：TCP 端口一旦开放，
// 任何人都能往里灌任意内容。Decode 里全是手写的长度前缀解析与偏移计算，
// 正是最容易写出越界/回绕的那类代码，也最不适合靠人力穷举用例。
//
// 断言的不变式：
//  1. 任何输入都不得 panic —— panic 会被 router 的 recover 兜住，
//     但在 transport 层是直接打崩 accept 协程
//  2. 解析成功时，各字段必须都落在 Body 之内（不能指向越界内存）
//
// 平时随 go test 跑种子语料；深挖用：
//
//	go test -fuzz=FuzzMessageDecode ./transport/
func FuzzMessageDecode(f *testing.F) {
	// 种子 1：一条合法消息
	valid := newMessage()
	valid.Path = "Test.Method"
	valid.Header["ServicePath"] = "Test"
	valid.Header["ServiceMethod"] = "Method"
	valid.Header["k"] = "v"
	valid.Payload = []byte("hello")
	f.Add(valid.Encode())

	// 种子 2：只有 Bom 和长度前缀
	head := make([]byte, 16)
	head[0] = MagicNumber
	f.Add(head)

	// 种子 3：谎报一个巨大的长度前缀（放大分配的经典手法）
	big := make([]byte, 16)
	big[0] = MagicNumber
	binary.BigEndian.PutUint32(big[12:16], 1<<30)
	f.Add(big)

	// 种子 4：字段长度前缀溢出 body
	overflow := make([]byte, 24)
	overflow[0] = MagicNumber
	binary.BigEndian.PutUint32(overflow[12:16], 8)
	binary.BigEndian.PutUint32(overflow[16:20], 0xFFFFFFFF)
	f.Add(overflow)

	// 种子 5：空输入与魔数错误
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Add([]byte{MagicNumber})

	f.Fuzz(func(t *testing.T, data []byte) {
		msg := newMessage()

		// 不得 panic。Decode 报错是完全正常的结果 —— 我们只关心它别炸。
		err := msg.Decode(bytes.NewReader(data))
		if err != nil {
			return
		}

		// 解析成功 ⇒ 各字段必须都落在 Body 之内。
		// 越界的切片会指向别的消息或已回收的内存，是最难查的一类串数据。
		body := msg.Body

		if len(msg.Path) > len(body) {
			t.Fatalf("Path 长度 %d 超出 Body %d", len(msg.Path), len(body))
		}
		if len(msg.Payload) > len(body) {
			t.Fatalf("Payload 长度 %d 超出 Body %d", len(msg.Payload), len(body))
		}
		if msg.Header == nil {
			t.Fatal("解析成功但 Header 为 nil，调用方会直接 nil map 读取")
		}

		// Payload 必须是 Body 的子切片
		if len(msg.Payload) > 0 && len(body) > 0 {
			if !isSubSlice(body, msg.Payload) {
				t.Fatal("Payload 不是 Body 的子切片 —— 指向了 Body 之外的内存")
			}
		}
	})
}

// FuzzMessageRoundTrip 验证 Encode→Decode 对任意字段内容都无损。
//
// 编解码是对称的，任何一侧的偏移算错都会在往返里暴露 —— 比单向解析更容易
// 命中"能解析成功但内容错位"这类静默损坏。
func FuzzMessageRoundTrip(f *testing.F) {
	f.Add("Test.Method", "Svc", "Mtd", "key", "value", []byte("payload"))
	f.Add("", "", "", "", "", []byte(nil))
	f.Add("路径/中文", "服务", "方法", "键", "值", []byte{0x00, 0xFF})

	f.Fuzz(func(t *testing.T, path, sp, sm, k, v string, payload []byte) {
		// ServicePath / ServiceMethod 走独立字段，其余进 metadata。
		// metadata 的键若与这两个保留名冲突，解码时会被覆盖，属预期行为，跳过。
		if k == "ServicePath" || k == "ServiceMethod" {
			return
		}

		msg := newMessage()
		msg.Path = path
		msg.Header["ServicePath"] = sp
		msg.Header["ServiceMethod"] = sm
		if k != "" {
			msg.Header[k] = v
		}
		msg.Payload = payload

		wire := msg.Encode()

		got := newMessage()
		if err := got.Decode(bytes.NewReader(wire)); err != nil {
			t.Fatalf("自己编码的消息解不出来: %v", err)
		}

		if got.Path != path {
			t.Errorf("Path = %q, want %q", got.Path, path)
		}
		if got.Header["ServicePath"] != sp {
			t.Errorf("ServicePath = %q, want %q", got.Header["ServicePath"], sp)
		}
		if got.Header["ServiceMethod"] != sm {
			t.Errorf("ServiceMethod = %q, want %q", got.Header["ServiceMethod"], sm)
		}
		if k != "" && got.Header[k] != v {
			t.Errorf("Header[%q] = %q, want %q", k, got.Header[k], v)
		}
		if !bytes.Equal(got.Payload, payload) {
			t.Errorf("Payload = %v, want %v", got.Payload, payload)
		}
	})
}

// FuzzDecodeMetadata 单独喂 metadata 解析 —— 它有独立的长度前缀循环，
// 且用减法比较来防整型回绕，值得单独压。
func FuzzDecodeMetadata(f *testing.F) {
	f.Add(encodeMetadata(map[string]string{"a": "b"}))
	f.Add([]byte{0, 0, 0, 4, 't', 'e', 's', 't'})
	f.Add([]byte{0xFF, 0xFF, 0xFF, 0xFF})
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		// 长度参数取实际长度：调用方（Decode）就是这么传的
		m, err := decodeMetadata(uint32(len(data)), data)
		if err != nil {
			return
		}
		if m == nil {
			t.Fatal("解析成功但返回 nil map")
		}
	})
}

// isSubSlice 判断 inner 是否指向 outer 内部的一段。
func isSubSlice(outer, inner []byte) bool {
	if len(inner) == 0 {
		return true
	}
	if len(inner) > len(outer) {
		return false
	}
	// 用容量末端定位：inner 若源自 outer，其底层数组末端必与 outer 一致
	return &outer[0:cap(outer)][cap(outer)-1] == &inner[0:cap(inner)][cap(inner)-1]
}
