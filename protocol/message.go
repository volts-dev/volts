package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/volts-dev/volts/codec"

	//log "github.com/volts-dev/logger"
	"github.com/volts-dev/utils"
)

var (
	// MaxMessageLength is the max length of a message.
	// Default is 0 that means does not limit length of messages.
	// It is used to validate when read messages from io.Reader.
	MaxMessageLength = 0

	// ErrMetaKVMissing some keys or values are mssing.
	ErrMetaKVMissing = errors.New("wrong metadata lines. some keys or values are missing")
	// ErrMessageToLong message is too long
	ErrMessageToLong = errors.New("message is too long")
)

const (
	MagicNumber byte = 0x08
	// ServiceError contains error info of service invocation
	ServiceError = "__rpcx_error__"

	// Request is message type of request
	Request MessageType = iota
	// Response is message type of response
	Response

	// Normal is normal requests and responses.
	Normal MessageStatusType = iota
	// Error indicates some errors occur.
	Error

	// None does not compress.
	None CompressType = iota
	// Gzip uses gzip compression.
	Gzip
)

// Message is the generic type of Request and Response.
type (
	// Header is the first part of Message and has fixed size.
	// Format:
	Header [12]byte
	// MessageType is message type of requests and resposnes.
	MessageType byte
	// MessageStatusType is status of messages.
	MessageStatusType byte
	// CompressType defines decompression type.
	CompressType byte

	TMessage struct {
		*Header
		Path          string // the path
		ServicePath   string //Route path
		ServiceMethod string //Route name
		Metadata      map[string]string
		Payload       []byte // 主题内容

		data []byte // 消息主体
	}
)

// NewMessage creates an empty message.
func NewMessage() *TMessage {
	header := Header([12]byte{})
	header[0] = MagicNumber

	return &TMessage{
		Header: &header,
	}
}

// CheckMagicNumber checks whether header starts rpcx magic number.
func (h Header) CheckMagicNumber() bool {
	return h[0] == MagicNumber
}

// Version returns version of rpcx protocol.
func (h Header) Version() byte {
	return h[1]
}

// SetVersion sets version for this header.
func (h *Header) SetVersion(v byte) {
	h[1] = v
}

// MessageType returns the message type.
func (h Header) MessageType() MessageType {
	return MessageType(h[2]&0x80) >> 7
}

// SetMessageType sets message type.
func (h *Header) SetMessageType(mt MessageType) {
	h[2] = h[2] | (byte(mt) << 7)
}

// IsHeartbeat returns whether the message is heartbeat message.
func (h Header) IsHeartbeat() bool {
	return h[2]&0x40 == 0x40
}

// SetHeartbeat sets the heartbeat flag.
func (h *Header) SetHeartbeat(hb bool) {
	if hb {
		h[2] = h[2] | 0x40
	} else {
		h[2] = h[2] &^ 0x40
	}
}

// IsOneway returns whether the message is one-way message.
// If true, server won't send responses.
func (h Header) IsOneway() bool {
	return h[2]&0x20 == 0x20
}

// SetOneway sets the oneway flag.
func (h *Header) SetOneway(oneway bool) {
	if oneway {
		h[2] = h[2] | 0x20
	} else {
		h[2] = h[2] &^ 0x20
	}
}

// CompressType returns compression type of messages.
func (h Header) CompressType() CompressType {
	return CompressType((h[2] & 0x1C) >> 2)
}

// SetCompressType sets the compression type.
func (h *Header) SetCompressType(ct CompressType) {
	h[2] = h[2] | ((byte(ct) << 2) & 0x1C)
}

// MessageStatusType returns the message status type.
func (h Header) MessageStatusType() MessageStatusType {
	return MessageStatusType(h[2] & 0x03)
}

// SetMessageStatusType sets message status type.
func (h *Header) SetMessageStatusType(mt MessageStatusType) {
	h[2] = h[2] | (byte(mt) & 0x03)
}

// SerializeType returns serialization type of payload.
func (h Header) SerializeType() codec.SerializeType {
	return codec.SerializeType((h[3] & 0xF0) >> 4)
}

// SetSerializeType sets the serialization type.
func (h *Header) SetSerializeType(st codec.SerializeType) {
	h[3] = h[3] | (byte(st) << 4)
}

// Seq returns sequence number of messages.
func (h Header) Seq() uint64 {
	return binary.BigEndian.Uint64(h[4:])
}

// SetSeq sets  sequence number.
func (h *Header) SetSeq(seq uint64) {
	binary.BigEndian.PutUint64(h[4:], seq)
}

// Clone clones from an message.
func (m TMessage) CloneTo(msg *TMessage) *TMessage {
	var header Header
	copy(header[:], m.Header[:])
	msg.Header = &header
	msg.ServicePath = m.ServicePath
	msg.ServiceMethod = m.ServiceMethod
	return msg
}

// Encode encodes messages.
func (m TMessage) Encode() []byte {
	meta := encodeMetadata(m.Metadata)

	path_len := len(m.Path)
	spL := len(m.ServicePath)
	smL := len(m.ServiceMethod)

	totalL := (4 + path_len) + (4 + spL) + (4 + smL) + (4 + len(meta)) + (4 + len(m.Payload))

	// header + dataLen + spLen + sp + smLen + sm + metaL + meta + payloadLen + payload
	metaStart := 12 + 4 + (4 + path_len) + (4 + spL) + (4 + smL)

	payLoadStart := metaStart + (4 + len(meta))
	l := 12 + 4 + totalL

	data := make([]byte, l)
	copy(data, m.Header[:])

	//totalLen

	binary.BigEndian.PutUint32(data[12:16], uint32(totalL))

	binary.BigEndian.PutUint32(data[16:20], uint32(path_len))
	copy(data[20:20+path_len], utils.StringToSliceByte(m.Path))

	bgn := 20 + path_len
	end := 24 + path_len
	binary.BigEndian.PutUint32(data[bgn:end], uint32(spL))
	copy(data[end:end+spL], utils.StringToSliceByte(m.ServicePath))

	bgn = end
	binary.BigEndian.PutUint32(data[bgn:bgn+4], uint32(smL))
	copy(data[bgn+4:metaStart], utils.StringToSliceByte(m.ServiceMethod))

	binary.BigEndian.PutUint32(data[metaStart:metaStart+4], uint32(len(meta)))
	copy(data[metaStart+4:], meta)

	binary.BigEndian.PutUint32(data[payLoadStart:payLoadStart+4], uint32(len(m.Payload)))
	copy(data[payLoadStart+4:], m.Payload)

	return data
}

// WriteTo writes message to writers.
func (m TMessage) WriteTo(w io.Writer) error {
	_, err := w.Write(m.Header[:])
	if err != nil {
		return err
	}

	meta := encodeMetadata(m.Metadata)

	spL := len(m.ServicePath)
	smL := len(m.ServiceMethod)

	totalL := (4 + spL) + (4 + smL) + (4 + len(meta)) + (4 + len(m.Payload))
	err = binary.Write(w, binary.BigEndian, uint32(totalL))
	if err != nil {
		return err
	}

	//write servicePath and serviceMethod
	err = binary.Write(w, binary.BigEndian, uint32(len(m.ServicePath)))
	if err != nil {
		return err
	}
	_, err = w.Write(utils.StringToSliceByte(m.ServicePath))
	if err != nil {
		return err
	}
	err = binary.Write(w, binary.BigEndian, uint32(len(m.ServiceMethod)))
	if err != nil {
		return err
	}
	_, err = w.Write(utils.StringToSliceByte(m.ServiceMethod))
	if err != nil {
		return err
	}

	// write meta
	err = binary.Write(w, binary.BigEndian, uint32(len(meta)))
	if err != nil {
		return err
	}
	_, err = w.Write(meta)
	if err != nil {
		return err
	}

	//write payload
	err = binary.Write(w, binary.BigEndian, uint32(len(m.Payload)))
	if err != nil {
		return err
	}

	_, err = w.Write(m.Payload)
	return err
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

// Read reads a message from r.
func Read(r io.Reader) (*TMessage, error) {
	msg := NewMessage()
	err := msg.Decode(r)
	if err != nil {
		return nil, err
	}
	return msg, nil
}

// Decode decodes a message from reader.
func (m *TMessage) Decode(r io.Reader) error {
	var err error
	// validate rest length for each step?
	//log.Dbg("TMessage.Decode", m.Header[:])

	//buf := make([]byte, 1)
	/*	_, err = r.Read(m.Header[:1])
		if err != nil {
			log.Dbg("TMessage.Decode", m.Header[:], err.Error())
			return err
		}*/

	// parse header
	_, err = io.ReadFull(r, m.Header[:1])
	if err != nil {
		return err
	}

	//log.Dbg("aafa", m.Header[:1], err)
	if !m.Header.CheckMagicNumber() {
		return fmt.Errorf("wrong magic number: %v", m.Header[0])
	}

	_, err = io.ReadFull(r, m.Header[1:])
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
	m.data = data

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
	m.ServicePath = utils.SliceByteToString(data[n:nEnd])
	n = nEnd

	// parse serviceMethod
	l = binary.BigEndian.Uint32(data[n : n+4])
	n = n + 4
	nEnd = n + int(l)
	m.ServiceMethod = utils.SliceByteToString(data[n:nEnd])
	n = nEnd

	// parse meta
	l = binary.BigEndian.Uint32(data[n : n+4])
	n = n + 4
	nEnd = n + int(l)

	if l > 0 {
		m.Metadata, err = decodeMetadata(l, data[n:nEnd])
		if err != nil {
			return err
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

// Reset clean data of this message but keep allocated data
func (m *TMessage) Reset() {
	resetHeader(m.Header)
	m.Metadata = nil
	m.Payload = m.Payload[:0]
	m.data = m.data[:0]
	m.ServicePath = ""
	m.ServiceMethod = ""
}

var zeroHeaderArray Header
var zeroHeader = zeroHeaderArray[1:]

func resetHeader(h *Header) {
	copy(h[1:], zeroHeader)
}
