package transport

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/volts-dev/utils"
	"github.com/volts-dev/volts/codec"
)

const (
	MagicNumber byte = 0x08

	// Request is message type of request
	MT_ERROR MessageType = iota
	MT_REQUEST
	MT_RESPONSE // Response is message type of response
)
const ( // TODO 9 重要修复状态修改不一致问题
	StatusOK MessageStatusType = iota // Normal Not an error; returned on success.
	// Error indicates some errors occur.
	StatusError
	//Internal errors. This means that some invariants expected by the underlying system have been broken. This error code is reserved for serious errors.
	StatusInternalError
	// The caller does not have permission to execute the specified operation. StatusForbidden must not be used for rejections caused by exhausting some resource (use RESOURCE_EXHAUSTED instead for those errors). StatusForbidden must not be used if the caller can not be identified (use UNAUTHENTICATED instead for those errors). This error code does not imply the request is valid or the requested entity exists or satisfies other pre-conditions.
	StatusForbidden
	StatusNotFound
	StatusUnknown
	StatusAborted
	// The operation is not implemented or is not supported/enabled in this service.
	StatusNotImplemented
	// The service is currently unavailable. This is most likely a transient condition, which can be corrected by retrying with a backoff. Note that it is not always safe to retry non-idempotent operations.
	StatusServiceUnavailable
	// The request does not have valid authentication credentials for the operation.
	StatusUnauthorized
)
const (
	// None does not compress.
	None CompressType = iota
	// Gzip uses gzip compression.
	Gzip
)

// DefaultMaxMessageLength is the safe default cap applied to incoming messages
// when callers do not override MaxMessageLength. 64 MiB matches common RPC framing
// upper bounds and prevents trivial OOM via a forged length prefix.
const DefaultMaxMessageLength = 64 << 20

// initialBodyBuffer 是解码消息体时的预分配上限。
//
// 长度前缀由对端指定，不能照着它一次性分配（见 Decode 里的说明）。这里只按
// "常规消息大小"起步，超出部分随实际到达的数据逐步扩容。
const initialBodyBuffer = 64 << 10

var ( // MaxMessageLength is the max length of a message.
	// Default is DefaultMaxMessageLength (64 MiB). Set to 0 to disable the cap.
	// It is used to validate when read messages from io.Reader.
	MaxMessageLength = DefaultMaxMessageLength
	// ErrMetaKVMissing some keys or values are mssing.
	ErrMetaKVMissing = errors.New("wrong metadata lines. some keys or values are missing")
	// ErrMessageToLong message is too long
	ErrMessageToLong = errors.New("message is too long")
	// ErrMessageMalformed indicates a length prefix that doesn't fit the body.
	ErrMessageMalformed = errors.New("malformed message: length prefix exceeds body")
)

type (
	// byte-order mark is the first part of Message and has fixed size.
	// Format:
	Bom [12]byte
	/**
	 *  Bom Protocol
	 *  +-------MagicNumber(1Byte)-------|------MessageType(Byte)--------+
	 *	+----------------------------------------------------------------+
	 *	| 0|                          LENGTH                             |
	 *	+----------------------------------------------------------------+
	 *	| 0|       HEADER MAGIC          |            FLAGS              |
	 *	+----------------------------------------------------------------+
	 *	|                         SEQUENCE NUMBER                        |
	 *	+----------------------------------------------------------------+
	 *	| 0|     Header Size(/32)        | ...
	 *	+---------------------------------
	 *
	 *	Message is of variable size:
	 *	(and starts at offset 14)
	 *
	 *	+----------------------------------------------------------------+
	 *	| Bom 12
	 *	+----------------------------------------------------------------+
	 *	|  Path
	 *	+----------------------------------------------------------------+
	 *	|         Header                  |
	 *	+----------------------------------------------------------------+
	 *	|        INFO 0 ID (uint8)      |       INFO 0  DATA ...
	 *	+----------------------------------------------------------------+
	 *	|         ...                              ...                   |
	 *	+----------------------------------------------------------------+
	 *	|                                                                |
	 *	|                              PAYLOAD                           |
	 *	|                                                                |
	 *	+----------------------------------------------------------------+
	 */
	// MessageType is message type of requests and resposnes.
	MessageType byte
	// MessageStatusType is status of messages.
	MessageStatusType byte
	// CompressType defines decompression type.
	CompressType byte

	IHeader interface {
		Add(key, value string)
		Set(key, value string)
		Get(key string) string
		//has(key string) bool
		Del(key string)
	}

	Message struct {
		*Bom // 字节码
		//Service  string            // service path
		//Endpoint string            // method path
		Path    string            // the path
		Header  map[string]string // 消息头
		Body    []byte            // 消息主体
		Payload []byte            // 消息主体中的内容

	}
)

// NewMessage creates an empty message.
func newMessage() *Message {
	bom := Bom([12]byte{})
	bom[0] = MagicNumber

	return &Message{
		Bom:    &bom,
		Header: make(map[string]string), //TODO 优化替代或者删除

	}
}

// Read reads a message from r.
func ReadMessage(r io.Reader) (*Message, error) {
	msg := newMessage()
	err := msg.Decode(r)
	if err != nil {
		return nil, err
	}
	return msg, nil
}

// CheckMagicNumber checks whether header starts rpc magic number.
func (self Bom) CheckMagicNumber() bool {
	return self[0] == MagicNumber
}

// Version returns version of rpc protocol.
func (self Bom) Version() byte {
	return (self[1] & 0xF0) >> 4
}

// SetVersion sets version for this Bom.
func (self *Bom) SetVersion(v byte) {
	self[1] = (self[1] &^ 0xF0) | ((v << 4) & 0xF0)
}

// MessageStatusType returns the message status type.
func (self Bom) MessageStatusType() MessageStatusType {
	return MessageStatusType(self[1] & 0x0F)
}

// SetMessageStatusType sets message status type.
func (self *Bom) SetMessageStatusType(mt MessageStatusType) {
	self[1] = (self[1] &^ 0x0F) | (byte(mt) & 0x0F)
}

// MessageType returns the message type.
func (self Bom) MessageType() MessageType {
	return MessageType((self[2] & 0xC0) >> 6)
}

// SetMessageType sets message type.
func (self *Bom) SetMessageType(mt MessageType) {
	self[2] = (self[2] &^ 0xC0) | ((byte(mt) << 6) & 0xC0)
}

// IsHeartbeat returns whether the message is heartbeat message.
func (self Bom) IsHeartbeat() bool {
	return self[2]&0x20 == 0x20
}

// SetHeartbeat sets the heartbeat flag.
func (self *Bom) SetHeartbeat(hb bool) {
	if hb {
		self[2] = self[2] | 0x20
	} else {
		self[2] = self[2] &^ 0x20
	}
}

// IsOneway returns whether the message is one-way message.
// If true, server won't send responses.
func (self Bom) IsOneway() bool {
	return self[2]&0x10 == 0x10
}

// SetOneway sets the oneway flag.
func (self *Bom) SetOneway(oneway bool) {
	if oneway {
		self[2] = self[2] | 0x10
	} else {
		self[2] = self[2] &^ 0x10
	}
}

// CompressType returns compression type of messages.
func (self Bom) CompressType() CompressType {
	return CompressType((self[2] & 0x0E) >> 1)
}

// SetCompressType sets the compression type.
func (self *Bom) SetCompressType(ct CompressType) {
	self[2] = (self[2] &^ 0x0E) | ((byte(ct) << 1) & 0x0E)
}

// SerializeType returns serialization type of payload.
func (self Bom) SerializeType() codec.SerializeType {
	return codec.SerializeType((self[3]))
}

// SetSerializeType sets the serialization type.
func (self *Bom) SetSerializeType(st codec.SerializeType) {
	self[3] = self[3] | byte(st)
}

// Seq returns sequence number of messages.
func (self Bom) Seq() uint64 {
	return binary.BigEndian.Uint64(self[4:])
}

// SetSeq sets  sequence number.
func (self *Bom) SetSeq(seq uint64) {
	binary.BigEndian.PutUint64(self[4:], seq)
}

var zeroHeaderArray Bom
var zeroHeader = zeroHeaderArray[1:]

func resetHeader(h *Bom) {
	copy(h[1:], zeroHeader)
}

// len,string,len,string,......
func encodeMetadata(m map[string]string) []byte {
	if len(m) == 0 {
		return []byte{}
	}
	var buf bytes.Buffer
	var d = make([]byte, 4)
	for k, v := range m {
		binary.BigEndian.PutUint32(d, uint32(len(k)))
		buf.Write(d)
		buf.Write(utils.StringToSliceByte(k))
		binary.BigEndian.PutUint32(d, uint32(len(v)))
		buf.Write(d)
		buf.Write(utils.StringToSliceByte(v))
	}
	return buf.Bytes()
}

func decodeMetadata(l uint32, data []byte) (map[string]string, error) {
	// 调用方 l 来自外部 length-prefix，可能与 data 不一致
	if uint32(len(data)) < l {
		return nil, ErrMetaKVMissing
	}

	m := make(map[string]string, 10)
	n := uint32(0)
	// 用减法 (l-n) 而非加法 (n+x) 比较，避免恶意 sl 触发 uint32 回绕。
	// 循环条件 n < l 保证 l-n > 0，每次递增前都先校验空间足够。
	for n < l {
		// key length
		if 4 > l-n {
			return nil, ErrMetaKVMissing
		}
		sl := binary.BigEndian.Uint32(data[n : n+4])
		n += 4
		if sl > l-n {
			return nil, ErrMetaKVMissing
		}
		k := utils.SliceByteToString(data[n : n+sl])
		n += sl

		// value length
		if 4 > l-n {
			return nil, ErrMetaKVMissing
		}
		sl = binary.BigEndian.Uint32(data[n : n+4])
		n += 4
		if sl > l-n {
			return nil, ErrMetaKVMissing
		}
		v := utils.SliceByteToString(data[n : n+sl])
		n += sl
		m[k] = v
	}

	return m, nil
}

// Encode encodes messages.
func (m Message) Encode() []byte {
	meta := encodeMetadata(m.Header)

	path_len := len(m.Path)
	spL := len(m.Header["ServicePath"])
	smL := len(m.Header["ServiceMethod"])

	totalL := (4 + path_len) + (4 + spL) + (4 + smL) + (4 + len(meta)) + (4 + len(m.Payload))

	// header + dataLen + spLen + sp + smLen + sm + metaL + meta + payloadLen + payload
	metaStart := 12 + 4 + (4 + path_len) + (4 + spL) + (4 + smL)

	payLoadStart := metaStart + (4 + len(meta))
	l := 12 + 4 + totalL

	data := make([]byte, l)
	copy(data, m.Bom[:])

	//totalLen

	binary.BigEndian.PutUint32(data[12:16], uint32(totalL))

	binary.BigEndian.PutUint32(data[16:20], uint32(path_len))
	copy(data[20:20+path_len], utils.StringToSliceByte(m.Path))

	bgn := 20 + path_len
	end := 24 + path_len
	binary.BigEndian.PutUint32(data[bgn:end], uint32(spL))
	copy(data[end:end+spL], utils.StringToSliceByte(m.Header["ServicePath"]))

	bgn = end + spL
	binary.BigEndian.PutUint32(data[bgn:bgn+4], uint32(smL))
	copy(data[bgn+4:metaStart], utils.StringToSliceByte(m.Header["ServiceMethod"]))

	binary.BigEndian.PutUint32(data[metaStart:metaStart+4], uint32(len(meta)))
	copy(data[metaStart+4:], meta)

	binary.BigEndian.PutUint32(data[payLoadStart:payLoadStart+4], uint32(len(m.Payload)))
	copy(data[payLoadStart+4:], m.Payload)

	return data
}

// Decode decodes a message from reader.
func (m *Message) Decode(r io.Reader) error {
	_, err := io.ReadFull(r, m.Bom[:1])
	if err != nil {
		return err
	}

	if !m.Bom.CheckMagicNumber() {
		return fmt.Errorf("wrong magic number: %v", m.Bom[0])
	}

	_, err = io.ReadFull(r, m.Bom[1:])
	if err != nil {
		return err
	}

	// total length
	lenData := poolUint32Data.Get().(*[]byte)
	_, err = io.ReadFull(r, *lenData)
	if err != nil {
		poolUint32Data.Put(lenData)
		return err
	}
	l := binary.BigEndian.Uint32(*lenData)
	poolUint32Data.Put(lenData)

	if MaxMessageLength > 0 && int(l) > MaxMessageLength {
		return ErrMessageToLong
	}

	// 按**实际到达的字节数**增长，而不是照着长度前缀一次性分配。
	//
	// 此前是 make([]byte, l) —— l 完全由对端指定，上限即 MaxMessageLength(64 MiB)。
	// 于是 16 字节的报文（Bom + 一个谎报 64 MiB 的长度前缀）就能让服务端立刻
	// 分配 64 MiB，随后 ReadFull 失败、连接关闭。放大倍数约 400 万倍，
	// 反复发送即可轻松打爆内存 —— 长度上限拦住的是"单条消息多大"，
	// 拦不住"用一个数字换一块内存"。
	//
	// bytes.Buffer 随 CopyN 逐步扩容，对端不发数据就不会占内存；预分配只取
	// 一个小的起步值，避免小消息反复扩容。
	var buf bytes.Buffer
	if l > 0 {
		buf.Grow(min(int(l), initialBodyBuffer))
	}
	if _, err = io.CopyN(&buf, r, int64(l)); err != nil {
		if err == io.EOF {
			// 声明了 l 字节却提前结束：按畸形包处理，与 ReadFull 的语义一致
			err = io.ErrUnexpectedEOF
		}
		return err
	}

	data := buf.Bytes()
	m.Body = data

	// readField 从 data[n:] 解析一段 length-prefixed 字节，返回字段 bytes 与新偏移；
	// 任何边界越界都返回 ErrMessageMalformed，避免触发 panic。
	// 用减法 (len(data)-n) 比较以避免 n+x 在恶意输入下整型回绕。
	readField := func(n int) ([]byte, int, error) {
		if 4 > len(data)-n {
			return nil, 0, ErrMessageMalformed
		}
		fl := int(binary.BigEndian.Uint32(data[n : n+4]))
		n += 4
		if fl < 0 || fl > len(data)-n {
			return nil, 0, ErrMessageMalformed
		}
		return data[n : n+fl], n + fl, nil
	}

	n := 0

	// parse Path
	field, n, err := readField(n)
	if err != nil {
		return err
	}
	m.Path = utils.SliceByteToString(field)

	// parse servicePath
	field, n, err = readField(n)
	if err != nil {
		return err
	}
	m.Header["ServicePath"] = utils.SliceByteToString(field)

	// parse serviceMethod
	field, n, err = readField(n)
	if err != nil {
		return err
	}
	m.Header["ServiceMethod"] = utils.SliceByteToString(field)

	// parse meta
	field, n, err = readField(n)
	if err != nil {
		return err
	}
	if len(field) > 0 {
		metadata, err := decodeMetadata(uint32(len(field)), field)
		if err != nil {
			return err
		}
		for k, v := range metadata {
			m.Header[k] = v
		}
	}

	// parse payload
	field, _, err = readField(n)
	if err != nil {
		return err
	}
	m.Payload = field

	return nil
}

// Clone clones from an message.
func (m Message) CloneTo(msg *Message) *Message {
	var bom Bom
	copy(bom[:], m.Bom[:])
	msg.Bom = &bom
	msg.Header["ServicePath"] = m.Header["ServicePath"]
	msg.Header["ServiceMethod"] = m.Header["ServiceMethod"]
	return msg
}

// Reset clean data of this message but keep allocated data
func (m *Message) Reset() {
	resetHeader(m.Bom)
	m.Header = make(map[string]string)
	m.Payload = m.Payload[:0]
	m.Body = m.Body[:0]
}
