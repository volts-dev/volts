package transport

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/volts-dev/volts/codec"
)

// 简单的 rpcHandler 实现，用于测试
type testRpcHandler struct{}

func (h *testRpcHandler) ServeRPC(rsp *RpcResponse, req *RpcRequest) {
	// 简单地回显请求数据
	rsp.WriteHeader(StatusOK)
	rsp.Write(req.Message.Payload)
}

func (h *testRpcHandler) String() string {
	return "testHandler"
}

func (h *testRpcHandler) Handler() interface{} {
	return h
}

// TestTcpRoundTrip 测试单次 TCP 请求/响应
func TestTcpRoundTrip(t *testing.T) {
	tr := NewTCPTransport(
		ReadTimeout(5*time.Second),
		WriteTimeout(5*time.Second),
	)

	// 启动服务端
	ln, err := tr.Listen(":0") // 随机端口
	if err != nil {
		t.Fatal("Listen error:", err)
	}
	defer ln.Close()

	handler := &testRpcHandler{}
	go func() {
		if err := ln.Serve(handler); err != nil {
			// listener closed 时会退出
		}
	}()

	addr := ln.Addr().String()
	t.Logf("Server listening on %s", addr)

	// 客户端发送一条消息
	conn, err := tr.Dial(addr, WithTimeout(5*time.Second, 5*time.Second, 5*time.Second))
	if err != nil {
		t.Fatal("Dial error:", err)
	}
	defer conn.Close()

	// 构建请求消息
	msg := newMessage()
	msg.SetMessageType(MT_REQUEST)
	msg.SetSerializeType(codec.JSON)
	msg.Path = "/test.echo"
	msg.Payload = []byte(`{"hello":"world"}`)

	// 发送
	if err := conn.Send(msg); err != nil {
		t.Fatal("Send error:", err)
	}

	// 接收响应
	resp := newMessage()
	if err := conn.Recv(resp); err != nil {
		t.Fatal("Recv error:", err)
	}

	if string(resp.Payload) != `{"hello":"world"}` {
		t.Fatalf("Unexpected response payload: %s", string(resp.Payload))
	}

	t.Log("Single round-trip: PASS")
}

// TestTcpConnectionReuse 测试同一连接上多次请求/响应（连接复用）
func TestTcpConnectionReuse(t *testing.T) {
	tr := NewTCPTransport(
		ReadTimeout(5*time.Second),
		WriteTimeout(5*time.Second),
	)

	ln, err := tr.Listen(":0")
	if err != nil {
		t.Fatal("Listen error:", err)
	}
	defer ln.Close()

	handler := &testRpcHandler{}
	go func() {
		ln.Serve(handler)
	}()

	addr := ln.Addr().String()
	t.Logf("Server listening on %s", addr)

	// 用同一个连接发送多次请求
	conn, err := tr.Dial(addr, WithTimeout(5*time.Second, 5*time.Second, 5*time.Second))
	if err != nil {
		t.Fatal("Dial error:", err)
	}
	defer conn.Close()

	for i := 0; i < 10; i++ {
		msg := newMessage()
		msg.SetMessageType(MT_REQUEST)
		msg.SetSerializeType(codec.JSON)
		msg.Path = "/test.echo"
		msg.Payload = []byte(fmt.Sprintf(`{"seq":%d}`, i))

		if err := conn.Send(msg); err != nil {
			t.Fatalf("Send #%d error: %v", i, err)
		}

		resp := newMessage()
		if err := conn.Recv(resp); err != nil {
			t.Fatalf("Recv #%d error: %v (THIS IS THE EOF BUG)", i, err)
		}

		expected := fmt.Sprintf(`{"seq":%d}`, i)
		if string(resp.Payload) != expected {
			t.Fatalf("Request #%d: expected %s, got %s", i, expected, string(resp.Payload))
		}
	}

	t.Log("Connection reuse (10 requests on same conn): PASS")
}

// TestTcpConcurrentConnections 测试并发多连接
func TestTcpConcurrentConnections(t *testing.T) {
	tr := NewTCPTransport(
		ReadTimeout(5*time.Second),
		WriteTimeout(5*time.Second),
	)

	ln, err := tr.Listen(":0")
	if err != nil {
		t.Fatal("Listen error:", err)
	}
	defer ln.Close()

	handler := &testRpcHandler{}
	go func() {
		ln.Serve(handler)
	}()

	addr := ln.Addr().String()

	var wg sync.WaitGroup
	errCh := make(chan error, 50)

	for c := 0; c < 5; c++ {
		wg.Add(1)
		go func(connId int) {
			defer wg.Done()

			conn, err := tr.Dial(addr, WithTimeout(5*time.Second, 5*time.Second, 5*time.Second))
			if err != nil {
				errCh <- fmt.Errorf("conn %d Dial error: %v", connId, err)
				return
			}
			defer conn.Close()

			for i := 0; i < 10; i++ {
				msg := newMessage()
				msg.SetMessageType(MT_REQUEST)
				msg.SetSerializeType(codec.JSON)
				msg.Path = "/test.echo"
				msg.Payload = []byte(fmt.Sprintf(`{"conn":%d,"seq":%d}`, connId, i))

				if err := conn.Send(msg); err != nil {
					errCh <- fmt.Errorf("conn %d Send #%d: %v", connId, i, err)
					return
				}

				resp := newMessage()
				if err := conn.Recv(resp); err != nil {
					errCh <- fmt.Errorf("conn %d Recv #%d: %v (EOF BUG)", connId, i, err)
					return
				}
			}
		}(c)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatal(err)
	}

	t.Log("Concurrent connections (5 conns x 10 reqs): PASS")
}
