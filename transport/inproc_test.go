package transport

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/volts-dev/volts/codec"
)

// pathRecordingHandler 回显载荷，并在后面标出这条请求是从哪种连接进来的。
type pathRecordingHandler struct{}

func (h *pathRecordingHandler) ServeRPC(rsp *RpcResponse, req *RpcRequest) {
	kind := "tcp"
	if c, ok := req.Context.Value(RemoteConnContextKey).(net.Conn); ok && c.RemoteAddr().Network() == "pipe" {
		kind = "pipe"
	}
	rsp.WriteHeader(StatusOK)
	rsp.Write(append(append([]byte{}, req.Message.Payload...), []byte("|"+kind)...))
}
func (h *pathRecordingHandler) String() string       { return "pathRecordingHandler" }
func (h *pathRecordingHandler) Handler() interface{} { return h }

func serveForTest(t *testing.T, opts ...Option) (IListener, <-chan struct{}) {
	t.Helper()
	tr := NewTCPTransport(append([]Option{ReadTimeout(5 * time.Second), WriteTimeout(5 * time.Second)}, opts...)...)
	ln, err := tr.Listen(":0")
	if err != nil {
		t.Fatal("Listen:", err)
	}
	done := make(chan struct{})
	go func() {
		_ = ln.Serve(&pathRecordingHandler{})
		close(done)
	}()
	// Serve 登记是异步的：等到表里出现再往下走，避免与拨号赛跑。
	waitFor(t, func() bool { return inprocFor(ln.Addr().String()) != nil || isSecure(opts) })
	return ln, done
}

func isSecure(opts []Option) bool {
	var c Config
	for _, o := range opts {
		o(&c)
	}
	return c.Secure
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 2s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func roundTrip(t *testing.T, c IClient, payload []byte) []byte {
	t.Helper()
	msg := newMessage()
	msg.SetMessageType(MT_REQUEST)
	msg.SetSerializeType(codec.JSON)
	msg.Path = "/test.echo"
	msg.Payload = payload
	if err := c.Send(msg); err != nil {
		t.Fatal("Send:", err)
	}
	resp := newMessage()
	if err := c.Recv(resp); err != nil {
		t.Fatal("Recv:", err)
	}
	return resp.Payload
}

// 同一个监听、同一组请求：默认走管道，DisableInproc 走 TCP，除了连接种类以外字节全同。
// 大载荷（2MiB）确认管道上整帧能过；同一连接连发确认连接可复用。
func TestInproc_LocalDialUsesPipeAndMatchesTCP(t *testing.T) {
	ln, _ := serveForTest(t)
	defer ln.Close()
	addr := ln.Addr().String()

	payloads := [][]byte{
		[]byte(`{"hello":"world"}`),
		bytes.Repeat([]byte("x"), 2<<20),
		[]byte(`{"seq":3}`),
	}

	dial := func(opts ...Option) IClient {
		c, err := NewTCPTransport(opts...).Dial(addr, WithTimeout(5*time.Second, 5*time.Second, 5*time.Second))
		if err != nil {
			t.Fatal("Dial:", err)
		}
		return c
	}
	viaPipe := dial()
	defer viaPipe.Close()
	viaTCP := dial(DisableInproc())
	defer viaTCP.Close()

	for i, p := range payloads {
		gotPipe := roundTrip(t, viaPipe, p)
		gotTCP := roundTrip(t, viaTCP, p)
		if !bytes.HasSuffix(gotPipe, []byte("|pipe")) {
			t.Fatalf("#%d: 本进程拨号没有走管道: suffix %q", i, tail(gotPipe))
		}
		if !bytes.HasSuffix(gotTCP, []byte("|tcp")) {
			t.Fatalf("#%d: DisableInproc 仍走了管道: suffix %q", i, tail(gotTCP))
		}
		if !bytes.Equal(bytes.TrimSuffix(gotPipe, []byte("|pipe")), bytes.TrimSuffix(gotTCP, []byte("|tcp"))) {
			t.Fatalf("#%d: 管道与 TCP 的应答不同", i)
		}
	}
}

func tail(b []byte) string {
	if len(b) > 8 {
		b = b[len(b)-8:]
	}
	return string(b)
}

// 服务端的空闲读超时对管道同样生效（slowloris 防护不因换了连接而失效），
// 且对端关闭后客户端读到的是 EOF —— 连接池据此丢弃死连接。
func TestInproc_ServerIdleTimeoutClosesPipe(t *testing.T) {
	tr := NewTCPTransport(ReadTimeout(100*time.Millisecond), WriteTimeout(5*time.Second))
	ln, err := tr.Listen(":0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go ln.Serve(&pathRecordingHandler{})
	waitFor(t, func() bool { return inprocFor(ln.Addr().String()) != nil })

	c, err := NewTCPTransport().Dial(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Conn().RemoteAddr().Network() != "pipe" {
		t.Fatalf("expected a pipe, got %s", c.Conn().RemoteAddr().Network())
	}

	done := make(chan error, 1)
	go func() {
		var b [1]byte
		_, err := c.Conn().Read(b[:])
		done <- err
	}()
	select {
	case err := <-done:
		if err != io.EOF {
			t.Fatalf("idle pipe closed with %v, want io.EOF", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not close the idle pipe — ReadTimeout not applied on the in-process path")
	}
}

// TLS 监听不登记：管道上没有证书，对端身份可能正由证书承担。
func TestInproc_TLSListenerIsNotRegistered(t *testing.T) {
	ln, _ := serveForTest(t, Secure(true))
	defer ln.Close()
	time.Sleep(50 * time.Millisecond) // 给 Serve 一个登记的机会（它不该登记）
	if inprocFor(ln.Addr().String()) != nil {
		t.Fatal("TLS listener was registered for in-process dialing")
	}
}

// 监听关了、Serve 返回之后，登记必须撤掉 —— 否则拨号会进一个已经不服务的循环。
func TestInproc_UnregisteredWhenServeReturns(t *testing.T) {
	ln, done := serveForTest(t)
	addr := ln.Addr().String()
	ln.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after Close")
	}
	if inprocFor(addr) != nil {
		t.Fatal("listener still registered after Serve returned")
	}
}

// 只认本机地址：别的主机上的同一个端口号不是本进程。
func TestInproc_RemoteHostWithSamePortIsNotLocal(t *testing.T) {
	ln, _ := serveForTest(t)
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	for _, host := range []string{"10.255.255.1", "203.0.113.7"} {
		if inprocFor(net.JoinHostPort(host, strconv.Itoa(port))) != nil {
			t.Fatalf("%s:%d treated as this process", host, port)
		}
	}
	for _, host := range []string{"127.0.0.1", "localhost", "::1", "0.0.0.0"} {
		if inprocFor(net.JoinHostPort(host, strconv.Itoa(port))) == nil {
			t.Fatal(fmt.Sprintf("%s:%d not recognised as this process", host, port))
		}
	}
}
