package client

import (
	"context"
	stderrors "errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/volts-dev/volts/errors"
)

// closedPort 返回一个刚刚被释放、当下没人监听的本地端口。
//
// 不用 RFC 5737 的不可路由地址：本机的网络扩展会**接受**每一个 TCP connect
// （实测 net.DialTimeout 到 192.0.2.1 在 0.01s 内成功），那样测出来的是扩展的行为
// 而不是拨号失败。回环地址不经过它。
func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	// 确认它真的连不上；连得上说明端口被别人抢了，这一轮没法测。
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err == nil {
		c.Close()
		t.Skipf("端口 %s 被占用了，跳过", addr)
	}
	return addr
}

// TestIsConnectFailure_OnlyDialPhase 锁定判据只认拨号阶段。
//
// 错误都由**真实的网络操作**产生，不是手搓的结构体 —— 手搓能证明的只是"我写的
// 判断和我写的构造一致"，证明不了标准库到底给的是什么形态。
func TestIsConnectFailure_OnlyDialPhase(t *testing.T) {
	// 拨号失败：连接被拒。
	_, dialErr := net.DialTimeout("tcp", closedPort(t), 2*time.Second)
	if dialErr == nil {
		t.Fatal("预期连接被拒")
	}
	if !isConnectFailure(dialErr) {
		t.Errorf("拨号失败没被认出来: %#v", dialErr)
	}

	// 经 http.Client 之后，错误被包在 *url.Error 里，仍须认得出。
	cli := &http.Client{Timeout: 2 * time.Second}
	_, httpErr := cli.Get("http://" + closedPort(t) + "/")
	if httpErr == nil {
		t.Fatal("预期请求失败")
	}
	if !isConnectFailure(httpErr) {
		t.Errorf("*url.Error 包装之后认不出拨号失败了: %#v", httpErr)
	}

	// 读响应阶段失败：连接建成、请求发出去了，服务端收下之后直接 RST。
	// 这一档**必须**判为不可重试 —— 分不清服务端有没有处理过。
	//
	// 用 SetLinger(0) 逼出 RST 而不是正常的 FIN：FIN 在客户端表现为 io.EOF，
	// 那根本不是 net.OpError，于是"只认 dial"这条限制就没被真正验到 —— 实测过，
	// 把判据放宽成"任何 net.OpError 都算"时用 FIN 的版本仍然全绿。RST 拿到的才是
	// net.OpError{Op:"read"}，正好卡在该限制上。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 1024)
			_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, _ = c.Read(buf) // 收下请求
			if tc, ok := c.(*net.TCPConn); ok {
				_ = tc.SetLinger(0) // Close 时发 RST
			}
			c.Close()
		}
	}()
	_, readErr := cli.Get("http://" + ln.Addr().String() + "/")
	if readErr == nil {
		t.Fatal("预期读响应失败")
	}
	var readOp *net.OpError
	if !stderrors.As(readErr, &readOp) || readOp.Op == "dial" {
		t.Skipf("没能造出读阶段的 net.OpError（拿到 %#v），本机网络行为不配合，跳过", readErr)
	}
	if isConnectFailure(readErr) {
		t.Errorf("请求已发出、读响应时断开，绝不能判为可重试: %#v", readErr)
	}

	if isConnectFailure(nil) {
		t.Error("nil 不该判为连接失败")
	}
}

// TestHttpClient_DialFailureCarriesSentinel 走完整客户端路径：拨不通的节点返回的
// 错误必须能被 RetryOnConnectError 认出来，同时仍然是一个带 500 码的 *errors.Error
// （调用方看到的东西不变）。
func TestHttpClient_DialFailureCarriesSentinel(t *testing.T) {
	addr := closedPort(t)
	cli, err := NewHttpClient()
	if err != nil {
		t.Fatal(err)
	}
	req, err := cli.NewRequest("GET", "http://"+addr+"/nowhere", nil)
	if err != nil {
		t.Fatal(err)
	}

	_, callErr := cli.Call(req)
	if callErr == nil {
		t.Fatal("预期调用失败")
	}
	if !errors.Is(callErr, ErrConnectFailed) {
		t.Fatalf("拨号失败没挂上 ErrConnectFailed 哨兵，RetryOnConnectError 会当它不可重试: %v", callErr)
	}
	if got := errors.Code(callErr); got != 500 {
		t.Errorf("状态码应保持 500，实际 %d", got)
	}

	retry, rerr := RetryOnConnectError(context.Background(), req, 0, callErr)
	if rerr != nil || !retry {
		t.Errorf("RetryOnConnectError 应判为可重试，实际 retry=%v err=%v", retry, rerr)
	}
}

// TestHttpClient_ServerErrorHasNoSentinel 锁定反向：服务端答复了（哪怕是 500），
// 就绝不是"连接没建成"，不能被判成可重试。
func TestHttpClient_ServerErrorHasNoSentinel(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`boom`))
	}))
	defer ts.Close()

	cli, err := NewHttpClient()
	if err != nil {
		t.Fatal(err)
	}
	req, err := cli.NewRequest("GET", ts.URL+"/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, callErr := cli.Call(req)
	if errors.Is(callErr, ErrConnectFailed) {
		t.Fatalf("服务端已经答复了，不该带连接失败哨兵: %v", callErr)
	}
}
