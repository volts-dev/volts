package transport

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// newWsTestPair 建一对管道：srv 是服务端视角的 WebSocket，cli 是裸连接，
// 测试用它直接投喂手工构造的帧。
func newWsTestPair(t *testing.T) (srv *TWebsocketConn, cli net.Conn) {
	t.Helper()
	c1, c2 := net.Pipe()
	t.Cleanup(func() {
		c1.Close()
		c2.Close()
	})
	// net.Pipe 无缓冲且不受 deadline 之外的保护，所有阻塞点都必须有超时，
	// 否则一处写错就是整包 300s 挂死。
	c1.SetDeadline(time.Now().Add(5 * time.Second))
	c2.SetDeadline(time.Now().Add(5 * time.Second))
	return NewWebsocketConn(c1, bufio.NewReader(c1), true, ""), c2
}

// buildFrame 手工构造一帧。masked 为真时按客户端规则加掩码。
func buildFrame(fin bool, rsv byte, opcode byte, payload []byte, masked bool) []byte {
	var buf bytes.Buffer
	b0 := opcode | (rsv << 4)
	if fin {
		b0 |= 0x80
	}
	buf.WriteByte(b0)

	n := len(payload)
	var b1 byte
	if masked {
		b1 = 0x80
	}
	switch {
	case n < 126:
		buf.WriteByte(b1 | byte(n))
	case n <= 0xFFFF:
		buf.WriteByte(b1 | 126)
		binary.Write(&buf, binary.BigEndian, uint16(n))
	default:
		buf.WriteByte(b1 | 127)
		binary.Write(&buf, binary.BigEndian, uint64(n))
	}

	body := append([]byte(nil), payload...)
	if masked {
		key := [4]byte{0xDE, 0xAD, 0xBE, 0xEF}
		buf.Write(key[:])
		wsMaskBytes(key, 0, body)
	}
	buf.Write(body)
	return buf.Bytes()
}

// feed 在后台把字节写进裸连接，避免 net.Pipe 的同步写阻塞测试主流程。
func feed(t *testing.T, cli net.Conn, data []byte) {
	t.Helper()
	go func() {
		cli.Write(data)
	}()
}

// drain 在后台读走服务端写出的字节，防止服务端因无人读取而阻塞。
func drain(cli net.Conn) chan []byte {
	ch := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 4096)
		n, _ := cli.Read(buf)
		ch <- buf[:n]
	}()
	return ch
}

func TestWsReadMessage_MaskedText(t *testing.T) {
	srv, cli := newWsTestPair(t)
	feed(t, cli, buildFrame(true, 0, WsOpText, []byte("hello"), true))

	op, data, err := srv.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage 出错: %v", err)
	}
	if op != WsOpText {
		t.Errorf("opcode = %d, 期望 WsOpText", op)
	}
	if string(data) != "hello" {
		t.Errorf("payload = %q, 期望 \"hello\"", data)
	}
}

func TestWsReadMessage_Fragmented(t *testing.T) {
	srv, cli := newWsTestPair(t)
	var b bytes.Buffer
	b.Write(buildFrame(false, 0, WsOpText, []byte("he"), true))
	b.Write(buildFrame(false, 0, WsOpContinuation, []byte("ll"), true))
	b.Write(buildFrame(true, 0, WsOpContinuation, []byte("o"), true))
	feed(t, cli, b.Bytes())

	op, data, err := srv.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage 出错: %v", err)
	}
	if op != WsOpText || string(data) != "hello" {
		t.Errorf("分片组装 = (%d,%q)，期望 (1,\"hello\")", op, data)
	}
}

// 控制帧允许插在分片消息中间，不得打断组装。
func TestWsReadMessage_ControlInterleavedInFragments(t *testing.T) {
	srv, cli := newWsTestPair(t)
	var b bytes.Buffer
	b.Write(buildFrame(false, 0, WsOpText, []byte("he"), true))
	b.Write(buildFrame(true, 0, WsOpPing, []byte("hi"), true))
	b.Write(buildFrame(true, 0, WsOpContinuation, []byte("llo"), true))
	feed(t, cli, b.Bytes())

	// 服务端会自动回 pong，得有人把它读走。
	pong := drain(cli)

	op, data, err := srv.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage 出错: %v", err)
	}
	if op != WsOpText || string(data) != "hello" {
		t.Errorf("分片组装 = (%d,%q)，期望 (1,\"hello\")", op, data)
	}

	select {
	case p := <-pong:
		if len(p) < 2 || p[0]&0x0F != WsOpPong {
			t.Errorf("期望自动回 pong，实得 % x", p)
		}
	case <-time.After(2 * time.Second):
		t.Error("超时：未收到自动 pong")
	}
}

// 服务端收到未掩码的客户端帧必须按协议错误处理（RFC 6455 §5.1）。
func TestWsReadMessage_RejectsUnmaskedClientFrame(t *testing.T) {
	srv, cli := newWsTestPair(t)
	drain(cli) // 服务端会回关闭帧
	feed(t, cli, buildFrame(true, 0, WsOpText, []byte("hello"), false))

	_, _, err := srv.ReadMessage()
	if err == nil {
		t.Fatal("未掩码帧被接受了，应报协议错误")
	}
	if !IsWsCloseCode(err, CloseProtocolError) {
		t.Errorf("err = %v，期望 CloseProtocolError", err)
	}
}

func TestWsReadMessage_RejectsReservedBits(t *testing.T) {
	srv, cli := newWsTestPair(t)
	drain(cli)
	feed(t, cli, buildFrame(true, 0x4, WsOpText, []byte("x"), true)) // rsv1=1

	_, _, err := srv.ReadMessage()
	if !IsWsCloseCode(err, CloseProtocolError) {
		t.Errorf("err = %v，期望 RSV 置位被拒", err)
	}
}

func TestWsReadMessage_RejectsReservedOpcode(t *testing.T) {
	srv, cli := newWsTestPair(t)
	drain(cli)
	feed(t, cli, buildFrame(true, 0, 0x3, []byte("x"), true))

	_, _, err := srv.ReadMessage()
	if !IsWsCloseCode(err, CloseProtocolError) {
		t.Errorf("err = %v，期望保留操作码被拒", err)
	}
}

// 控制帧不得分片。
func TestWsReadMessage_RejectsFragmentedControlFrame(t *testing.T) {
	srv, cli := newWsTestPair(t)
	drain(cli)
	feed(t, cli, buildFrame(false, 0, WsOpPing, []byte("x"), true))

	_, _, err := srv.ReadMessage()
	if !IsWsCloseCode(err, CloseProtocolError) {
		t.Errorf("err = %v，期望分片控制帧被拒", err)
	}
}

// 控制帧载荷上限 125 字节。
func TestWsReadMessage_RejectsOversizeControlFrame(t *testing.T) {
	srv, cli := newWsTestPair(t)
	drain(cli)
	feed(t, cli, buildFrame(true, 0, WsOpPing, bytes.Repeat([]byte("x"), 126), true))

	_, _, err := srv.ReadMessage()
	if !IsWsCloseCode(err, CloseProtocolError) {
		t.Errorf("err = %v，期望超长控制帧被拒", err)
	}
}

// 单帧超过读上限必须在分配前就被拦住。
func TestWsReadMessage_EnforcesReadLimit(t *testing.T) {
	srv, cli := newWsTestPair(t)
	srv.SetReadLimit(16)
	drain(cli)
	feed(t, cli, buildFrame(true, 0, WsOpBinary, bytes.Repeat([]byte("x"), 64), true))

	_, _, err := srv.ReadMessage()
	if !errors.Is(err, ErrWsMessageTooBig) {
		t.Errorf("err = %v，期望 ErrWsMessageTooBig", err)
	}
}

// 每片都不超限、但累加超限的分片消息同样必须被拦住。
// 只在 readFrame 里卡一次是不够的 —— 这正是分片攻击的形状。
func TestWsReadMessage_EnforcesLimitOnFragmentAccumulation(t *testing.T) {
	srv, cli := newWsTestPair(t)
	srv.SetReadLimit(16)
	drain(cli)

	var b bytes.Buffer
	b.Write(buildFrame(false, 0, WsOpBinary, bytes.Repeat([]byte("a"), 10), true))
	b.Write(buildFrame(true, 0, WsOpContinuation, bytes.Repeat([]byte("b"), 10), true))
	feed(t, cli, b.Bytes())

	_, _, err := srv.ReadMessage()
	if !errors.Is(err, ErrWsMessageTooBig) {
		t.Errorf("err = %v，期望分片累加触发 ErrWsMessageTooBig", err)
	}
}

// 文本帧载荷必须是合法 UTF-8。
func TestWsReadMessage_RejectsInvalidUTF8(t *testing.T) {
	srv, cli := newWsTestPair(t)
	drain(cli)
	feed(t, cli, buildFrame(true, 0, WsOpText, []byte{0xC3, 0x28}, true))

	_, _, err := srv.ReadMessage()
	if err == nil {
		t.Fatal("非法 UTF-8 文本帧被接受了")
	}
}

// 二进制帧不做 UTF-8 校验。
func TestWsReadMessage_BinaryAllowsArbitraryBytes(t *testing.T) {
	srv, cli := newWsTestPair(t)
	payload := []byte{0xC3, 0x28, 0x00, 0xFF}
	feed(t, cli, buildFrame(true, 0, WsOpBinary, payload, true))

	op, data, err := srv.ReadMessage()
	if err != nil {
		t.Fatalf("二进制帧不该报错: %v", err)
	}
	if op != WsOpBinary || !bytes.Equal(data, payload) {
		t.Errorf("二进制帧 = (%d,% x)，期望原样返回", op, data)
	}
}

func TestWsReadMessage_CloseFrameReturnsCloseError(t *testing.T) {
	srv, cli := newWsTestPair(t)
	drain(cli) // 关闭握手的回送

	payload := make([]byte, 0, 8)
	payload = binary.BigEndian.AppendUint16(payload, CloseGoingAway)
	payload = append(payload, "bye"...)
	feed(t, cli, buildFrame(true, 0, WsOpClose, payload, true))

	_, _, err := srv.ReadMessage()
	var ce *TWsCloseError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v，期望 TWsCloseError", err)
	}
	if ce.Code != CloseGoingAway || ce.Text != "bye" {
		t.Errorf("关闭帧解析 = (%d,%q)，期望 (1001,\"bye\")", ce.Code, ce.Text)
	}
}

// 1 字节的关闭载荷非法：状态码固定 2 字节。
func TestWsReadMessage_RejectsOneBytePayloadCloseFrame(t *testing.T) {
	srv, cli := newWsTestPair(t)
	drain(cli)
	feed(t, cli, buildFrame(true, 0, WsOpClose, []byte{0x03}, true))

	_, _, err := srv.ReadMessage()
	if !IsWsCloseCode(err, CloseProtocolError) {
		t.Errorf("err = %v，期望 1 字节关闭载荷被拒", err)
	}
}

// 1005/1006 是本地保留码，不允许出现在线路上。
func TestWsReadMessage_RejectsReservedCloseCode(t *testing.T) {
	for _, code := range []uint16{CloseNoStatusReceived, CloseAbnormalClosure, 999, 1016, 5000} {
		srv, cli := newWsTestPair(t)
		drain(cli)
		payload := binary.BigEndian.AppendUint16(nil, code)
		feed(t, cli, buildFrame(true, 0, WsOpClose, payload, true))

		_, _, err := srv.ReadMessage()
		if !IsWsCloseCode(err, CloseProtocolError) {
			t.Errorf("关闭码 %d: err = %v，期望被判非法", code, err)
		}
	}
}

// 服务端发出的帧不得带掩码位。
func TestWsWriteMessage_ServerFramesUnmasked(t *testing.T) {
	srv, cli := newWsTestPair(t)
	got := drain(cli)

	go srv.WriteMessage(WsOpText, []byte("hi"))

	select {
	case b := <-got:
		if len(b) < 2 {
			t.Fatalf("帧太短: % x", b)
		}
		if b[0] != 0x81 {
			t.Errorf("首字节 = 0x%02X，期望 0x81 (FIN|text)", b[0])
		}
		if b[1]&0x80 != 0 {
			t.Error("服务端帧带了掩码位，违反 RFC 6455 §5.1")
		}
		if string(b[2:]) != "hi" {
			t.Errorf("载荷 = %q，期望 \"hi\"", b[2:])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("超时：没读到服务端帧")
	}
}

// WriteMessage 不接受控制操作码，WriteControl 不接受数据操作码。
func TestWsWrite_OpcodeGuards(t *testing.T) {
	srv, _ := newWsTestPair(t)
	if err := srv.WriteMessage(WsOpPing, nil); err == nil {
		t.Error("WriteMessage 接受了控制操作码")
	}
	if err := srv.WriteControl(WsOpText, nil, time.Time{}); err == nil {
		t.Error("WriteControl 接受了数据操作码")
	}
	if err := srv.WriteControl(WsOpPing, bytes.Repeat([]byte("x"), 126), time.Time{}); err == nil {
		t.Error("WriteControl 接受了超过 125 字节的载荷")
	}
}

// 客户端方向必须掩码，且不得就地改写调用方的切片。
func TestWsWriteMessage_ClientMasksWithoutMutatingCallerBuffer(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	c1.SetDeadline(time.Now().Add(5 * time.Second))
	c2.SetDeadline(time.Now().Add(5 * time.Second))

	cliConn := NewWebsocketConn(c1, bufio.NewReader(c1), false, "")
	payload := []byte("hello")
	original := append([]byte(nil), payload...)

	got := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 128)
		n, _ := c2.Read(buf)
		got <- buf[:n]
	}()

	if err := cliConn.WriteMessage(WsOpText, payload); err != nil {
		t.Fatalf("WriteMessage 出错: %v", err)
	}

	if !bytes.Equal(payload, original) {
		t.Errorf("调用方缓冲被就地掩码改写了: %q", payload)
	}

	select {
	case b := <-got:
		if b[1]&0x80 == 0 {
			t.Error("客户端帧未掩码，违反 RFC 6455 §5.3")
		}
		var key [4]byte
		copy(key[:], b[2:6])
		body := append([]byte(nil), b[6:]...)
		wsMaskBytes(key, 0, body)
		if string(body) != "hello" {
			t.Errorf("解掩码后 = %q，期望 \"hello\"", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("超时：没读到客户端帧")
	}
}

// 扩展长度字段：126 走 uint16，>65535 走 uint64。
func TestWsWriteMessage_ExtendedLengthEncoding(t *testing.T) {
	for _, size := range []int{125, 126, 65535, 65536} {
		c1, c2 := net.Pipe()
		c1.SetDeadline(time.Now().Add(5 * time.Second))
		c2.SetDeadline(time.Now().Add(5 * time.Second))

		srv := NewWebsocketConn(c1, bufio.NewReader(c1), true, "")
		go srv.WriteMessage(WsOpBinary, bytes.Repeat([]byte("z"), size))

		// 用一个客户端视角的 Conn 把它读回来，顺带验证读写自洽。
		cli := NewWebsocketConn(c2, bufio.NewReader(c2), false, "")
		op, data, err := cli.ReadMessage()
		if err != nil {
			t.Fatalf("size=%d 读回失败: %v", size, err)
		}
		if op != WsOpBinary || len(data) != size {
			t.Errorf("size=%d: 读回 (%d,%d 字节)", size, op, len(data))
		}
		c1.Close()
		c2.Close()
	}
}

// WriteClose 只应发出一次关闭帧，重复调用是空操作。
func TestWsWriteClose_OnlySentOnce(t *testing.T) {
	srv, cli := newWsTestPair(t)
	got := drain(cli)

	go func() {
		srv.WriteClose(CloseNormalClosure, "done")
		srv.WriteClose(CloseNormalClosure, "again")
	}()

	select {
	case b := <-got:
		if b[0]&0x0F != WsOpClose {
			t.Errorf("首帧不是关闭帧: % x", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("超时：没读到关闭帧")
	}
}

// Close 可重复调用。
func TestWsClose_Idempotent(t *testing.T) {
	srv, _ := newWsTestPair(t)
	if err := srv.Close(); err != nil {
		t.Fatalf("首次 Close 出错: %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Errorf("重复 Close 应为空操作，实得: %v", err)
	}
	if err := srv.WriteMessage(WsOpText, []byte("x")); !errors.Is(err, ErrWsClosed) {
		t.Errorf("已关闭连接上写入 err = %v，期望 ErrWsClosed", err)
	}
}

func TestWsValidCloseCode(t *testing.T) {
	valid := []int{1000, 1001, 1002, 1003, 1007, 1008, 1009, 1010, 1011, 1012, 1013, 1014, 3000, 4999}
	for _, c := range valid {
		if !wsValidCloseCode(c) {
			t.Errorf("关闭码 %d 应合法", c)
		}
	}
	// 1004 保留未定义、1005/1006 仅本地产生、1015 保留，均不得上线。
	invalid := []int{0, 999, 1004, 1005, 1006, 1015, 1016, 2999, 5000}
	for _, c := range invalid {
		if wsValidCloseCode(c) {
			t.Errorf("关闭码 %d 应非法", c)
		}
	}
}

// 掩码是自逆运算，且分块处理时相位必须连续。
func TestWsMaskBytes_PhaseContinuity(t *testing.T) {
	key := [4]byte{1, 2, 3, 4}
	data := []byte(strings.Repeat("abcdefg", 5))
	whole := append([]byte(nil), data...)
	wsMaskBytes(key, 0, whole)

	chunked := append([]byte(nil), data...)
	pos := wsMaskBytes(key, 0, chunked[:3])
	wsMaskBytes(key, pos, chunked[3:])

	if !bytes.Equal(whole, chunked) {
		t.Error("分块掩码与整体掩码结果不一致，相位没接上")
	}

	wsMaskBytes(key, 0, whole)
	if !bytes.Equal(whole, data) {
		t.Error("掩码不是自逆运算")
	}
}
