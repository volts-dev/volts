package transport

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"

	"github.com/volts-dev/volts/codec"
)

func TestBom_Flags(t *testing.T) {
	msg := newMessage()
	bom := msg.Bom

	// MagicNumber
	if !bom.CheckMagicNumber() {
		t.Errorf("Expected MagicNumber to be valid")
	}

	// Version
	bom.SetVersion(2)
	if bom.Version() != 2 {
		t.Errorf("Expected Version 2, got %v", bom.Version())
	}

	// MessageType
	bom.SetMessageType(MT_REQUEST)
	if bom.MessageType() != MT_REQUEST {
		t.Errorf("Expected MT_REQUEST, got %v", bom.MessageType())
	}

	// Heartbeat
	bom.SetHeartbeat(true)
	if !bom.IsHeartbeat() {
		t.Errorf("Expected Heartbeat true")
	}
	bom.SetHeartbeat(false)
	if bom.IsHeartbeat() {
		t.Errorf("Expected Heartbeat false")
	}

	// Oneway
	bom.SetOneway(true)
	if !bom.IsOneway() {
		t.Errorf("Expected Oneway true")
	}
	bom.SetOneway(false)
	if bom.IsOneway() {
		t.Errorf("Expected Oneway false")
	}

	// CompressType
	bom.SetCompressType(Gzip)
	if bom.CompressType() != Gzip {
		t.Errorf("Expected Gzip, got %v", bom.CompressType())
	}

	// MessageStatusType max width check
	bom.SetMessageStatusType(StatusUnauthorized)
	if bom.MessageStatusType() != StatusUnauthorized {
		t.Errorf("Expected StatusUnauthorized (value 8), got %v", bom.MessageStatusType())
	}

	// SerializeType
	st := codec.Use("json")
	bom.SetSerializeType(st)
	if bom.SerializeType() != st {
		t.Errorf("Expected serialize type %v, got %v", st, bom.SerializeType())
	}

	// Seq
	bom.SetSeq(123456789)
	if bom.Seq() != 123456789 {
		t.Errorf("Expected Seq 123456789, got %v", bom.Seq())
	}
}

func TestEncodeDecodeMetadata(t *testing.T) {
	m := map[string]string{
		"key1": "value1",
		"key2": "value2",
	}

	b := encodeMetadata(m)
	m2, err := decodeMetadata(uint32(len(b)), b)
	if err != nil {
		t.Fatalf("decodeMetadata failed: %v", err)
	}

	if !reflect.DeepEqual(m, m2) {
		t.Errorf("Expected %v, got %v", m, m2)
	}

	// Test empty
	mEmpty := make(map[string]string)
	bEmpty := encodeMetadata(mEmpty)
	m2Empty, err := decodeMetadata(0, bEmpty)
	if err != nil {
		t.Fatalf("decodeMetadata empty failed: %v", err)
	}
	if len(m2Empty) != 0 {
		t.Errorf("Expected empty map, got %v", m2Empty)
	}
}

func TestMessage_EncodeDecode(t *testing.T) {
	msg := newMessage()
	msg.Bom.SetMessageType(MT_REQUEST)
	msg.Bom.SetSeq(1)
	msg.Path = "Test.Method"
	msg.Header["ServicePath"] = "Test"
	msg.Header["ServiceMethod"] = "Method"
	msg.Header["CustomKey"] = "CustomValue"
	msg.Payload = []byte("hello world")

	data := msg.Encode()

	msg2 := newMessage()
	err := msg2.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}

	if msg2.Path != msg.Path {
		t.Errorf("Expected Path %v, got %v", msg.Path, msg2.Path)
	}
	if msg2.Header["ServicePath"] != msg.Header["ServicePath"] {
		t.Errorf("Expected ServicePath %v, got %v", msg.Header["ServicePath"], msg2.Header["ServicePath"])
	}
	if msg2.Header["ServiceMethod"] != msg.Header["ServiceMethod"] {
		t.Errorf("Expected ServiceMethod %v, got %v", msg.Header["ServiceMethod"], msg2.Header["ServiceMethod"])
	}
	if msg2.Header["CustomKey"] != msg.Header["CustomKey"] {
		t.Errorf("Expected CustomKey %v, got %v", msg.Header["CustomKey"], msg2.Header["CustomKey"])
	}
	if !bytes.Equal(msg2.Payload, msg.Payload) {
		t.Errorf("Expected Payload %v, got %v", msg.Payload, msg2.Payload)
	}
	if msg2.Bom.Seq() != msg.Bom.Seq() {
		t.Errorf("Expected Seq %v, got %v", msg.Bom.Seq(), msg2.Bom.Seq())
	}
}

func TestMessage_CloneTo(t *testing.T) {
	msg := newMessage()
	msg.Bom.SetSeq(100)
	msg.Header["ServicePath"] = "Svc"
	msg.Header["ServiceMethod"] = "Mtd"

	msg2 := newMessage()
	msg.CloneTo(msg2)

	if msg2.Bom.Seq() != msg.Bom.Seq() {
		t.Errorf("Expected Seq %v, got %v", msg.Bom.Seq(), msg2.Bom.Seq())
	}
	if msg2.Header["ServicePath"] != msg.Header["ServicePath"] {
		t.Errorf("Expected ServicePath %v, got %v", msg.Header["ServicePath"], msg2.Header["ServicePath"])
	}
	if msg2.Header["ServiceMethod"] != msg.Header["ServiceMethod"] {
		t.Errorf("Expected ServiceMethod %v, got %v", msg.Header["ServiceMethod"], msg2.Header["ServiceMethod"])
	}
}

func TestMessage_Reset(t *testing.T) {
	msg := newMessage()
	msg.Bom.SetSeq(100)
	msg.Path = "A.B"
	msg.Header["K"] = "V"
	msg.Payload = []byte("payload")
	msg.Body = []byte("body")

	msg.Reset()

	if msg.Bom.Seq() != 0 {
		t.Errorf("Expected Seq 0, got %v", msg.Bom.Seq())
	}
	if len(msg.Header) != 0 {
		t.Errorf("Expected empty Header, got %v", msg.Header)
	}
	if len(msg.Payload) != 0 {
		t.Errorf("Expected empty Payload, got %v", msg.Payload)
	}
	if len(msg.Body) != 0 {
		t.Errorf("Expected empty Body, got %v", msg.Body)
	}
}

func TestReadMessage(t *testing.T) {
	msg := newMessage()
	msg.Payload = []byte("test read message")
	msg.Header["Test"] = "Reading"
	data := msg.Encode()

	msg2, err := ReadMessage(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ReadMessage failed: %v", err)
	}

	if !bytes.Equal(msg2.Payload, msg.Payload) {
		t.Errorf("Expected Payload %v, got %v", msg.Payload, msg2.Payload)
	}
	if msg2.Header["Test"] != msg.Header["Test"] {
		t.Errorf("Expected Header Test %v, got %v", msg.Header["Test"], msg2.Header["Test"])
	}
}

func TestDecodeMessage_TooLong(t *testing.T) {
	msg := newMessage()
	msg.Payload = make([]byte, 1024)
	data := msg.Encode()

	MaxMessageLength = 512
	defer func() {
		MaxMessageLength = 0
	}()

	msg2 := newMessage()
	err := msg2.Decode(bytes.NewReader(data))
	if err != ErrMessageToLong {
		t.Fatalf("Expected ErrMessageToLong, got %v", err)
	}
}

func TestDecode_WrongMagicNumber(t *testing.T) {
	msg := newMessage()
	data := msg.Encode()
	data[0] = 0x00 // Set wrong magic number

	msg2 := newMessage()
	err := msg2.Decode(bytes.NewReader(data))
	if err == nil {
		t.Fatalf("Expected error due to wrong magic number, got nil")
	}
}

func TestDecodeMetadata_MissingKv(t *testing.T) {
	meta := []byte{0, 0, 0, 4, 't', 'e', 's', 't'} // Only key, no value length
	_, err := decodeMetadata(uint32(len(meta)), meta)
	if err != ErrMetaKVMissing {
		t.Fatalf("Expected ErrMetaKVMissing, got %v", err)
	}
}

// === Malformed message tests (C2 hardening) ===

// helper: 构造一条合法消息后截断 body 到 n 字节，模拟畸形包。
// 返回截断后的完整 wire bytes（含 Bom + totalLen + 截断 body）。
func truncatedMessage(t *testing.T, bodyLen int) []byte {
	t.Helper()
	msg := newMessage()
	msg.Path = "Test.Method"
	msg.Header["ServicePath"] = "Test"
	msg.Header["ServiceMethod"] = "Method"
	msg.Payload = []byte("hello world")
	data := msg.Encode()
	// data: [12]Bom + [4]totalLen + body...
	if bodyLen > len(data)-16 {
		bodyLen = len(data) - 16
	}
	// 重写 totalLen 为截断后的长度，使 Decode 走到 body 解析阶段
	out := make([]byte, 16+bodyLen)
	copy(out, data[:16])
	binary.BigEndian.PutUint32(out[12:16], uint32(bodyLen))
	copy(out[16:], data[16:16+bodyLen])
	return out
}

func TestDecode_TruncatedPathLength(t *testing.T) {
	// body 只有 2 字节，连 path 长度字段（4 字节）都读不到
	data := truncatedMessage(t, 2)
	msg := newMessage()
	err := msg.Decode(bytes.NewReader(data))
	if err == nil {
		t.Fatalf("expected error for truncated path length, got nil")
	}
}

func TestDecode_PathLengthOverflowsBody(t *testing.T) {
	// 构造 body：path_len 字段声明 1<<20 字节，但实际 body 远小于此
	body := make([]byte, 8)
	binary.BigEndian.PutUint32(body[0:4], 1<<20) // 谎报 path 长度
	wire := make([]byte, 16+len(body))
	wire[0] = MagicNumber
	binary.BigEndian.PutUint32(wire[12:16], uint32(len(body)))
	copy(wire[16:], body)

	msg := newMessage()
	err := msg.Decode(bytes.NewReader(wire))
	if err == nil {
		t.Fatalf("expected error for path length overflowing body, got nil")
	}
}

func TestDecode_TruncatedServicePath(t *testing.T) {
	// 合法 path 后，service path 长度字段不完整
	body := make([]byte, 0, 32)
	body = binary.BigEndian.AppendUint32(body, 4) // path_len = 4
	body = append(body, "abcd"...)
	body = append(body, 0x00, 0x00) // 只剩 2 字节，凑不齐 servicePath 长度
	wire := make([]byte, 16+len(body))
	wire[0] = MagicNumber
	binary.BigEndian.PutUint32(wire[12:16], uint32(len(body)))
	copy(wire[16:], body)

	msg := newMessage()
	err := msg.Decode(bytes.NewReader(wire))
	if err == nil {
		t.Fatalf("expected error for truncated servicePath length, got nil")
	}
}

func TestDecodeMetadata_LengthUnderflow(t *testing.T) {
	// 触发原代码 `n+sl > l-4` 的 uint32 下溢：l=2 时 l-4 回绕成巨大值
	meta := []byte{0x00, 0x00} // 仅 2 字节
	_, err := decodeMetadata(2, meta)
	if err == nil {
		t.Fatalf("expected error for metadata length=2 (underflow case), got nil")
	}
}

func TestDecodeMetadata_KeyLengthOverflowsData(t *testing.T) {
	// 谎报 key 长度 = 1000，但只给 4 字节数据
	meta := make([]byte, 4)
	binary.BigEndian.PutUint32(meta, 1000)
	_, err := decodeMetadata(uint32(len(meta)), meta)
	if err == nil {
		t.Fatalf("expected error for key length overflowing data, got nil")
	}
}
