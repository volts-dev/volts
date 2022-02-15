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

	// Normal is normal requests and responses.
	Normal MessageStatusType = iota
	// Error indicates some errors occur.
	Error

	// None does not compress.
	None CompressType = iota
	// Gzip uses gzip compression.
	Gzip
)

var ( // MaxMessageLength is the max length of a message.
	// Default is 0 that means does not limit length of messages.
	// It is used to validate when read messages from io.Reader.
	MaxMessageLength = 0
	// ErrMetaKVMissing some keys or values are mssing.
	ErrMetaKVMissing = errors.New("wrong metadata lines. some keys or values are missing")
	// ErrMessageToLong message is too long
	ErrMessageToLong = errors.New("message is too long")
)

type (
	// byte-order mark is the first part of Message and has fixed size.
	// Format:
	Bom [12]byte

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
		*Bom                      // 字节码
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
	return self[1]
}

// SetVersion sets version for this Bom.
func (self *Bom) SetVersion(v byte) {
	self[1] = v
}

// MessageType returns the message type.
func (self Bom) MessageType() MessageType {
	return MessageType(self[2]&0x80) >> 7
}

// SetMessageType sets message type.
func (self *Bom) SetMessageType(mt MessageType) {
	self[2] = self[2] | (byte(mt) << 7)
}

// IsHeartbeat returns whether the message is heartbeat message.
func (self Bom) IsHeartbeat() bool {
	return self[2]&0x40 == 0x40
}

// SetHeartbeat sets the heartbeat flag.
func (self *Bom) SetHeartbeat(hb bool) {
	if hb {
		self[2] = self[2] | 0x40
	} else {
		self[2] = self[2] &^ 0x40
	}
}

// IsOneway returns whether the message is one-way message.
// If true, server won't send responses.
func (self Bom) IsOneway() bool {
	return self[2]&0x20 == 0x20
}

// SetOneway sets the oneway flag.
func (self *Bom) SetOneway(oneway bool) {
	if oneway {
		self[2] = self[2] | 0x20
	} else {
		self[2] = self[2] &^ 0x20
	}
}

// CompressType returns compression type of messages.
func (self Bom) CompressType() CompressType {
	return CompressType((self[2] & 0x1C) >> 2)
}

// SetCompressType sets the compression type.
func (self *Bom) SetCompressType(ct CompressType) {
	self[2] = self[2] | ((byte(ct) << 2) & 0x1C)
}

// MessageStatusType returns the message status type.
func (self Bom) MessageStatusType() MessageStatusType {
	return MessageStatusType(self[2] & 0x03)
}

// SetMessageStatusType sets message status type.
func (self *Bom) SetMessageStatusType(mt MessageStatusType) {
	self[2] = self[2] | (byte(mt) & 0x03)
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
	m := make(map[string]string, 10)
	n := uint32(0)
	for n < l {
		// parse one key and value
		// key
		sl := binary.BigEndian.Uint32(data[n : n+4])
		n = n + 4
		if n+sl > l-4 {
			return m, ErrMetaKVMissing
		}
		k := utils.SliceByteToString(data[n : n+sl])
		n = n + sl

		// value
		sl = binary.BigEndian.Uint32(data[n : n+4])
		n = n + 4
		if n+sl > l {
			return m, ErrMetaKVMissing
		}
		v := utils.SliceByteToString(data[n : n+sl])
		n = n + sl
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

	bgn = end
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
	var err error
	// validate rest length for each step?
	//log.Dbg("TMessage.Decode", m.Bom[:])

	//buf := make([]byte, 1)
	/*	_, err = r.Read(m.Bom[:1])
		if err != nil {
			log.Dbg("TMessage.Decode", m.Bom[:], err.Error())
			return err
		}*/

	// parse Bom
	_, err = io.ReadFull(r, m.Bom[:1])
	if err != nil {
		return err
	}

	//log.Dbg("aafa", m.Bom[:1], err)
	if !m.Bom.CheckMagicNumber() {
		return fmt.Errorf("wrong magic number: %v", m.Bom[0])
	}

	_, err = io.ReadFull(r, m.Bom[1:])
	if err != nil {
		return err
	}

	//total
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

	data := make([]byte, int(l))
	_, err = io.ReadFull(r, data)
	if err != nil {
		return err
	}
	m.Body = data

	n := 0
	// parse Path
	l = binary.BigEndian.Uint32(data[n:4])
	n = n + 4
	nEnd := n + int(l)
	m.Path = utils.SliceByteToString(data[n:nEnd])
	n = nEnd

	// parse servicePath
	l = binary.BigEndian.Uint32(data[n : n+4])
	n = n + 4
	nEnd = n + int(l)
	m.Header["ServicePath"] = utils.SliceByteToString(data[n:nEnd])
	n = nEnd

	// parse serviceMethod
	l = binary.BigEndian.Uint32(data[n : n+4])
	n = n + 4
	nEnd = n + int(l)
	m.Header["ServiceMethod"] = utils.SliceByteToString(data[n:nEnd])
	n = nEnd

	// parse meta
	l = binary.BigEndian.Uint32(data[n : n+4])
	n = n + 4
	nEnd = n + int(l)

	if l > 0 {
		metadata, err := decodeMetadata(l, data[n:nEnd])
		if err != nil {
			return err
		}
		for k, v := range metadata {
			m.Header[k] = v
		}
	}
	n = nEnd

	// parse payload
	l = binary.BigEndian.Uint32(data[n : n+4])
	_ = l
	n = n + 4
	m.Payload = data[n:]

	return err
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
