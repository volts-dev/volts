package transport

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
	"unicode/utf8"
)

// RFC 6455 操作码。
const (
	WsOpContinuation = 0x0
	WsOpText         = 0x1
	WsOpBinary       = 0x2
	WsOpClose        = 0x8
	WsOpPing         = 0x9
	WsOpPong         = 0xA
)

// RFC 6455 §7.4.1 关闭状态码。1005/1006 只能由本地产生，不得出现在线路上。
const (
	CloseNormalClosure           = 1000
	CloseGoingAway               = 1001
	CloseProtocolError           = 1002
	CloseUnsupportedData         = 1003
	CloseNoStatusReceived        = 1005
	CloseAbnormalClosure         = 1006
	CloseInvalidFramePayloadData = 1007
	ClosePolicyViolation         = 1008
	CloseMessageTooBig           = 1009
	CloseMandatoryExtension      = 1010
	CloseInternalServerErr       = 1011
	CloseServiceRestart          = 1012
	CloseTryAgainLater           = 1013
	CloseBadGateway              = 1014
	CloseTLSHandshake            = 1015
)

const (
	// 控制帧载荷上限，RFC 6455 §5.5 硬性规定。
	wsMaxControlPayload = 125

	// 默认单条消息上限 32MB。**这个限制不是可选项**：帧头里的载荷长度是对端
	// 说了算的 uint64，不设上限就等于让任何一个 socket 声明 16EB 然后由我们
	// 去 make([]byte, n) —— 和 codec 里限制 gob 是同一类内存放大问题。
	wsDefaultReadLimit = 32 << 20
)

var (
	ErrWsClosed        = errors.New("websocket: connection closed")
	ErrWsMessageTooBig = errors.New("websocket: message exceeds read limit")
	// 收到关闭帧后 ReadMessage 返回它，调用方据此正常收尾而不是当作故障。
	ErrWsCloseReceived = errors.New("websocket: close frame received")
)

// TWsCloseError 携带对端关闭帧里的状态码与原因。
type TWsCloseError struct {
	Code int
	Text string
}

func (self *TWsCloseError) Error() string {
	return fmt.Sprintf("websocket: close %d (%s)", self.Code, self.Text)
}

// IsWsCloseCode 判断 err 是否为指定状态码的关闭错误，便于调用方区分正常下线与异常断开。
func IsWsCloseCode(err error, codes ...int) bool {
	var ce *TWsCloseError
	if !errors.As(err, &ce) {
		return false
	}
	for _, c := range codes {
		if ce.Code == c {
			return true
		}
	}
	return false
}

// wsFrame 是一个已解码的帧头加载荷。
type wsFrame struct {
	fin     bool
	rsv1    bool
	rsv2    bool
	rsv3    bool
	opcode  byte
	masked  bool
	payload []byte
}

// TWebsocketConn 是一条已完成握手的 WebSocket 连接。
//
// 并发约定与 gorilla 一致：**同一时刻只允许一个读者、一个写者**。读由单个
// goroutine 独占调用 ReadMessage；写侧 WriteMessage/WriteControl 内部有锁，
// 因此多个 goroutine 并发发送是安全的（bus 广播需要这一点）。
type TWebsocketConn struct {
	conn net.Conn
	br   *bufio.Reader
	bw   *bufio.Writer

	// isServer 决定掩码方向：服务端发出的帧不得掩码，客户端发出的帧必须掩码。
	isServer bool

	subprotocol string

	wmu sync.Mutex // 保护 bw 与写超时，允许并发写
	rmu sync.Mutex // 只为防御性串行化读，正常路径单 goroutine 读

	readLimit int64

	// 分片消息组装状态，仅读 goroutine 访问。
	fragOpcode byte
	fragBuf    []byte

	closeMu   sync.Mutex
	closed    bool
	closeSent bool

	// 收到 ping/pong 时的回调，nil 表示使用默认行为（自动回 pong）。
	pingHandler  func(appData string) error
	pongHandler  func(appData string) error
	closeHandler func(code int, text string) error
}

// NewWebsocketConn 用一条已劫持的连接构造 WebSocket。br 可以是握手阶段
// bufio.ReadWriter 里的读缓冲——**必须复用**，否则客户端紧跟 101 之后
// 发来的、已经被读进缓冲区的字节会丢失。
func NewWebsocketConn(conn net.Conn, br *bufio.Reader, isServer bool, subprotocol string) *TWebsocketConn {
	if br == nil {
		br = bufio.NewReader(conn)
	}
	return &TWebsocketConn{
		conn:        conn,
		br:          br,
		bw:          bufio.NewWriter(conn),
		isServer:    isServer,
		subprotocol: subprotocol,
		readLimit:   wsDefaultReadLimit,
	}
}

func (self *TWebsocketConn) String() string {
	return "WebsocketConn"
}

// Subprotocol 返回握手阶段协商出的子协议，未协商时为空串。
func (self *TWebsocketConn) Subprotocol() string {
	return self.subprotocol
}

func (self *TWebsocketConn) LocalAddr() net.Addr  { return self.conn.LocalAddr() }
func (self *TWebsocketConn) RemoteAddr() net.Addr { return self.conn.RemoteAddr() }

// NetConn 暴露底层连接，仅供需要设置 socket 选项的场景使用。
func (self *TWebsocketConn) NetConn() net.Conn { return self.conn }

// SetReadLimit 设置单条消息（含分片合并后）的最大字节数，<=0 表示恢复默认值。
func (self *TWebsocketConn) SetReadLimit(n int64) {
	if n <= 0 {
		n = wsDefaultReadLimit
	}
	self.readLimit = n
}

func (self *TWebsocketConn) SetReadDeadline(t time.Time) error {
	return self.conn.SetReadDeadline(t)
}

func (self *TWebsocketConn) SetWriteDeadline(t time.Time) error {
	return self.conn.SetWriteDeadline(t)
}

// SetPingHandler 覆盖收到 ping 时的行为。默认自动回 pong 并原样带回载荷。
func (self *TWebsocketConn) SetPingHandler(h func(appData string) error) {
	self.pingHandler = h
}

// SetPongHandler 覆盖收到 pong 时的行为，默认忽略。心跳保活在此刷新读超时。
func (self *TWebsocketConn) SetPongHandler(h func(appData string) error) {
	self.pongHandler = h
}

// SetCloseHandler 覆盖收到关闭帧时的行为，默认回送相同状态码完成关闭握手。
func (self *TWebsocketConn) SetCloseHandler(h func(code int, text string) error) {
	self.closeHandler = h
}

// readFrame 读取并解码一帧，同时执行 RFC 6455 的帧级校验。
func (self *TWebsocketConn) readFrame() (*wsFrame, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(self.br, hdr[:]); err != nil {
		return nil, err
	}

	f := &wsFrame{
		fin:    hdr[0]&0x80 != 0,
		rsv1:   hdr[0]&0x40 != 0,
		rsv2:   hdr[0]&0x20 != 0,
		rsv3:   hdr[0]&0x10 != 0,
		opcode: hdr[0] & 0x0F,
		masked: hdr[1]&0x80 != 0,
	}

	// 未协商任何扩展，RSV 必须为 0（RFC 6455 §5.2）。permessage-deflate 若日后
	// 接入，需要在这里放行 rsv1。
	if f.rsv1 || f.rsv2 || f.rsv3 {
		return nil, self.protocolError("reserved bits set")
	}

	switch f.opcode {
	case WsOpContinuation, WsOpText, WsOpBinary, WsOpClose, WsOpPing, WsOpPong:
	default:
		return nil, self.protocolError(fmt.Sprintf("reserved opcode 0x%X", f.opcode))
	}

	isControl := f.opcode&0x08 != 0

	length := int64(hdr[1] & 0x7F)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(self.br, ext[:]); err != nil {
			return nil, err
		}
		length = int64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(self.br, ext[:]); err != nil {
			return nil, err
		}
		u := binary.BigEndian.Uint64(ext[:])
		// 最高位必须为 0（RFC 6455 §5.2），且不能溢出 int64。
		if u > 1<<63-1 {
			return nil, self.protocolError("payload length MSB set")
		}
		length = int64(u)
	}

	if isControl {
		// 控制帧不得分片，载荷不得超过 125 字节。
		if !f.fin {
			return nil, self.protocolError("fragmented control frame")
		}
		if length > wsMaxControlPayload {
			return nil, self.protocolError("control frame payload too large")
		}
	}

	// 服务端收到的帧必须掩码，客户端收到的帧必须不掩码（RFC 6455 §5.1）。
	if self.isServer && !f.masked {
		return nil, self.protocolError("unmasked frame from client")
	}
	if !self.isServer && f.masked {
		return nil, self.protocolError("masked frame from server")
	}

	// 在分配之前先卡上限：这里是唯一由对端数字驱动的 make。
	if length > self.readLimit {
		self.writeCloseNoWait(CloseMessageTooBig, "message too big")
		return nil, ErrWsMessageTooBig
	}

	var maskKey [4]byte
	if f.masked {
		if _, err := io.ReadFull(self.br, maskKey[:]); err != nil {
			return nil, err
		}
	}

	if length > 0 {
		f.payload = make([]byte, length)
		if _, err := io.ReadFull(self.br, f.payload); err != nil {
			return nil, err
		}
		if f.masked {
			wsMaskBytes(maskKey, 0, f.payload)
		}
	}

	return f, nil
}

// ReadMessage 读取一条完整消息，自动组装分片并处理穿插其中的控制帧。
// 返回的 opcode 只会是 WsOpText 或 WsOpBinary。
func (self *TWebsocketConn) ReadMessage() (opcode int, data []byte, err error) {
	self.rmu.Lock()
	defer self.rmu.Unlock()

	for {
		f, err := self.readFrame()
		if err != nil {
			return 0, nil, err
		}

		// 控制帧可以插在分片消息中间，就地处理后继续读。
		if f.opcode&0x08 != 0 {
			if err := self.handleControl(f); err != nil {
				return 0, nil, err
			}
			continue
		}

		switch f.opcode {
		case WsOpText, WsOpBinary:
			if self.fragOpcode != 0 {
				return 0, nil, self.protocolError("expected continuation frame")
			}
			if f.fin {
				if f.opcode == WsOpText && !utf8.Valid(f.payload) {
					self.writeCloseNoWait(CloseInvalidFramePayloadData, "invalid utf-8")
					return 0, nil, self.protocolError("invalid utf-8 in text frame")
				}
				return int(f.opcode), f.payload, nil
			}
			self.fragOpcode = f.opcode
			self.fragBuf = f.payload

		case WsOpContinuation:
			if self.fragOpcode == 0 {
				return 0, nil, self.protocolError("unexpected continuation frame")
			}
			if int64(len(self.fragBuf))+int64(len(f.payload)) > self.readLimit {
				// 分片可以逐帧都很小却累加成天文数字，合并时必须再卡一次。
				self.fragOpcode, self.fragBuf = 0, nil
				self.writeCloseNoWait(CloseMessageTooBig, "message too big")
				return 0, nil, ErrWsMessageTooBig
			}
			self.fragBuf = append(self.fragBuf, f.payload...)
			if f.fin {
				op, buf := self.fragOpcode, self.fragBuf
				self.fragOpcode, self.fragBuf = 0, nil
				if op == WsOpText && !utf8.Valid(buf) {
					self.writeCloseNoWait(CloseInvalidFramePayloadData, "invalid utf-8")
					return 0, nil, self.protocolError("invalid utf-8 in text message")
				}
				return int(op), buf, nil
			}
		}
	}
}

// handleControl 处理 ping/pong/close 三种控制帧。
func (self *TWebsocketConn) handleControl(f *wsFrame) error {
	switch f.opcode {
	case WsOpPing:
		if self.pingHandler != nil {
			return self.pingHandler(string(f.payload))
		}
		// 默认原样回 pong。对端已经走了或写超时都不该让读循环崩掉。
		err := self.WriteControl(WsOpPong, f.payload, time.Now().Add(5*time.Second))
		if err == ErrWsClosed || errors.Is(err, net.ErrClosed) {
			return err
		}
		return nil

	case WsOpPong:
		if self.pongHandler != nil {
			return self.pongHandler(string(f.payload))
		}
		return nil

	case WsOpClose:
		code := CloseNoStatusReceived
		text := ""
		switch {
		case len(f.payload) == 1:
			// 1 字节关闭载荷是非法的：状态码固定 2 字节。
			return self.protocolError("invalid close payload length")
		case len(f.payload) >= 2:
			code = int(binary.BigEndian.Uint16(f.payload[:2]))
			text = string(f.payload[2:])
			if !wsValidCloseCode(code) {
				self.writeCloseNoWait(CloseProtocolError, "invalid close code")
				return &TWsCloseError{Code: CloseProtocolError, Text: "invalid close code"}
			}
			if !utf8.ValidString(text) {
				self.writeCloseNoWait(CloseInvalidFramePayloadData, "invalid utf-8")
				return &TWsCloseError{Code: CloseInvalidFramePayloadData, Text: "invalid utf-8 in close reason"}
			}
		}

		if self.closeHandler != nil {
			if err := self.closeHandler(code, text); err != nil {
				return err
			}
		} else {
			// 完成关闭握手：回送同样的状态码。对端可能已经关了 socket，忽略写错误。
			echo := code
			if echo == CloseNoStatusReceived {
				echo = CloseNormalClosure
			}
			self.writeCloseNoWait(echo, "")
		}
		return &TWsCloseError{Code: code, Text: text}
	}
	return nil
}

// WriteMessage 发送一条完整消息（不分片）。opcode 应为 WsOpText 或 WsOpBinary。
func (self *TWebsocketConn) WriteMessage(opcode int, data []byte) error {
	if opcode&0x08 != 0 {
		return errors.New("websocket: WriteMessage does not accept control opcodes, use WriteControl")
	}
	return self.writeFrame(byte(opcode), data, true)
}

// WriteControl 发送控制帧，载荷不得超过 125 字节。deadline 为零值表示不设写超时。
func (self *TWebsocketConn) WriteControl(opcode int, data []byte, deadline time.Time) error {
	if opcode&0x08 == 0 {
		return errors.New("websocket: WriteControl only accepts control opcodes")
	}
	if len(data) > wsMaxControlPayload {
		return errors.New("websocket: control frame payload exceeds 125 bytes")
	}

	self.wmu.Lock()
	defer self.wmu.Unlock()
	if self.isClosed() {
		return ErrWsClosed
	}
	if !deadline.IsZero() {
		self.conn.SetWriteDeadline(deadline)
		defer self.conn.SetWriteDeadline(time.Time{})
	}
	return self.writeFrameLocked(byte(opcode), data, true)
}

func (self *TWebsocketConn) writeFrame(opcode byte, data []byte, fin bool) error {
	self.wmu.Lock()
	defer self.wmu.Unlock()
	if self.isClosed() {
		return ErrWsClosed
	}
	return self.writeFrameLocked(opcode, data, fin)
}

// writeFrameLocked 必须在持有 wmu 时调用。
func (self *TWebsocketConn) writeFrameLocked(opcode byte, data []byte, fin bool) error {
	var hdr [14]byte
	n := 2

	hdr[0] = opcode
	if fin {
		hdr[0] |= 0x80
	}

	length := len(data)
	switch {
	case length < 126:
		hdr[1] = byte(length)
	case length <= 0xFFFF:
		hdr[1] = 126
		binary.BigEndian.PutUint16(hdr[2:4], uint16(length))
		n = 4
	default:
		hdr[1] = 127
		binary.BigEndian.PutUint64(hdr[2:10], uint64(length))
		n = 10
	}

	var maskKey [4]byte
	if !self.isServer {
		// 客户端发出的帧必须掩码，掩码键必须不可预测（RFC 6455 §5.3）。
		if _, err := rand.Read(maskKey[:]); err != nil {
			return err
		}
		hdr[1] |= 0x80
		copy(hdr[n:n+4], maskKey[:])
		n += 4
	}

	if _, err := self.bw.Write(hdr[:n]); err != nil {
		return err
	}

	if length > 0 {
		if self.isServer {
			if _, err := self.bw.Write(data); err != nil {
				return err
			}
		} else {
			// 不能就地掩码调用方的切片——那会改写调用方持有的数据。
			masked := make([]byte, length)
			copy(masked, data)
			wsMaskBytes(maskKey, 0, masked)
			if _, err := self.bw.Write(masked); err != nil {
				return err
			}
		}
	}

	return self.bw.Flush()
}

// WriteClose 发送关闭帧并完成本端的关闭握手，随后仍需调用 Close 释放 socket。
func (self *TWebsocketConn) WriteClose(code int, text string) error {
	self.closeMu.Lock()
	if self.closeSent {
		self.closeMu.Unlock()
		return nil
	}
	self.closeSent = true
	self.closeMu.Unlock()

	payload := make([]byte, 0, 2+len(text))
	payload = binary.BigEndian.AppendUint16(payload, uint16(code))
	payload = append(payload, text...)
	return self.WriteControl(WsOpClose, payload, time.Now().Add(5*time.Second))
}

// writeCloseNoWait 尽力发送关闭帧，忽略错误——用于协议错误路径，
// 此时连接反正要断，写不出去也没有补救手段。
func (self *TWebsocketConn) writeCloseNoWait(code int, text string) {
	self.WriteClose(code, text)
}

// Close 关闭底层连接。重复调用安全。
func (self *TWebsocketConn) Close() error {
	self.closeMu.Lock()
	if self.closed {
		self.closeMu.Unlock()
		return nil
	}
	self.closed = true
	self.closeMu.Unlock()
	return self.conn.Close()
}

func (self *TWebsocketConn) isClosed() bool {
	self.closeMu.Lock()
	defer self.closeMu.Unlock()
	return self.closed
}

func (self *TWebsocketConn) protocolError(reason string) error {
	self.writeCloseNoWait(CloseProtocolError, reason)
	return &TWsCloseError{Code: CloseProtocolError, Text: reason}
}

// wsValidCloseCode 判断线路上出现的关闭码是否合法。
//
// 三类码不得出现在关闭帧里：1004 在 RFC 6455 §7.4.1 中保留且至今未定义语义；
// 1005/1006 只能由本地产生（“没收到状态码”“异常断开”）；1015 同为保留码。
// 合法区间取 IANA 注册表的 1000-1014（去掉上述几个）与私有段 3000-4999。
func wsValidCloseCode(code int) bool {
	switch {
	case code >= 3000 && code <= 4999:
		return true
	case code >= 1000 && code <= 1014:
		return code != 1004 &&
			code != CloseNoStatusReceived &&
			code != CloseAbnormalClosure
	default:
		return false
	}
}

// wsMaskBytes 就地异或掩码。pos 是 b 在整条载荷中的起始偏移，用于分块处理时保持相位。
func wsMaskBytes(key [4]byte, pos int, b []byte) int {
	for i := range b {
		b[i] ^= key[pos&3]
		pos++
	}
	return pos & 3
}
