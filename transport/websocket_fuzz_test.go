package transport

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

// nopConn 把一个 bytes.Reader 包装成 net.Conn，写入全部丢弃。
// 帧解码路径在协议错误时会尝试回送关闭帧，没有这个写端就会阻塞。
type nopConn struct {
	r io.Reader
}

func (c *nopConn) Read(b []byte) (int, error)         { return c.r.Read(b) }
func (c *nopConn) Write(b []byte) (int, error)        { return len(b), nil }
func (c *nopConn) Close() error                       { return nil }
func (c *nopConn) LocalAddr() net.Addr                { return nil }
func (c *nopConn) RemoteAddr() net.Addr               { return nil }
func (c *nopConn) SetDeadline(t time.Time) error      { return nil }
func (c *nopConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *nopConn) SetWriteDeadline(t time.Time) error { return nil }

// FuzzWebsocketFrameDecode 用任意字节喂帧解码器。
//
// 和 FuzzMessageDecode 同理：WebSocket 帧头是纯手写的位运算加长度前缀解析，
// 其中 127 分支直接把对端给的 uint64 当作长度用。这里要钉死的不变式是
// **任何输入都不得 panic，也不得据此分配超过读上限的内存**——后者不是
// 理论风险：不设限时一个 10 字节的帧头就能让进程去申请 16EB。
func FuzzWebsocketFrameDecode(f *testing.F) {
	// 种子：合法文本帧、分片、控制帧、以及几个畸形头。
	f.Add(buildFrame(true, 0, WsOpText, []byte("hello"), true))
	f.Add(buildFrame(false, 0, WsOpBinary, []byte("frag"), true))
	f.Add(buildFrame(true, 0, WsOpPing, []byte("p"), true))
	f.Add(buildFrame(true, 0, WsOpClose, []byte{0x03, 0xE8}, true))
	f.Add([]byte{0x81, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}) // 长度 MSB 置位
	f.Add([]byte{0x81, 0xFE, 0xFF, 0xFF})                                     // 声明 65535 但无载荷
	f.Add([]byte{0xF1, 0x80, 0x00, 0x00, 0x00, 0x00})                         // RSV 全置位
	f.Add([]byte{0x00})

	f.Fuzz(func(t *testing.T, data []byte) {
		conn := &nopConn{r: bytes.NewReader(data)}
		ws := NewWebsocketConn(conn, bufio.NewReader(conn), true, "")
		ws.SetReadLimit(1 << 16)

		// 反复读到出错为止：单帧不出错时要继续走分片组装路径。
		for i := 0; i < 64; i++ {
			_, payload, err := ws.ReadMessage()
			if err != nil {
				break
			}
			if int64(len(payload)) > ws.readLimit {
				t.Fatalf("返回载荷 %d 字节，超过读上限 %d", len(payload), ws.readLimit)
			}
		}
	})
}

// FuzzWebsocketRoundTrip 断言自编自解的自洽性：任意载荷经 WriteMessage 编码后，
// 用客户端视角的 Conn 读回必须与原文逐字节相等。
func FuzzWebsocketRoundTrip(f *testing.F) {
	f.Add([]byte("hello"))
	f.Add([]byte{})
	f.Add(bytes.Repeat([]byte("x"), 126))
	f.Add(bytes.Repeat([]byte("y"), 70000))

	f.Fuzz(func(t *testing.T, payload []byte) {
		if len(payload) > 1<<20 {
			t.Skip("载荷过大，无关协议正确性")
		}

		var buf bytes.Buffer
		// 服务端方向写出不带掩码的帧。
		srv := NewWebsocketConn(&nopConn{r: &buf}, bufio.NewReader(&buf), true, "")
		srv.bw = bufio.NewWriter(&buf)
		if err := srv.WriteMessage(WsOpBinary, payload); err != nil {
			t.Fatalf("WriteMessage 出错: %v", err)
		}

		// 客户端方向读回：正好也验证了“客户端收到的帧必须不掩码”这条校验没写反。
		rd := &nopConn{r: bytes.NewReader(buf.Bytes())}
		cli := NewWebsocketConn(rd, bufio.NewReader(rd), false, "")
		cli.SetReadLimit(1 << 21)

		op, got, err := cli.ReadMessage()
		if err != nil {
			t.Fatalf("读回失败: %v", err)
		}
		if op != WsOpBinary {
			t.Fatalf("opcode = %d，期望 WsOpBinary", op)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("往返不一致: 得 %d 字节，原 %d 字节", len(got), len(payload))
		}
	})
}
