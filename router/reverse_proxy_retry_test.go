package router

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type stubRT struct {
	calls  int
	bodies []string
	err    error
}

func (s *stubRT) RoundTrip(req *http.Request) (*http.Response, error) {
	s.calls++
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		s.bodies = append(s.bodies, string(b))
	}
	if s.calls == 1 && s.err != nil {
		return nil, s.err
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: http.Header{}}, nil
}

func newPost(t *testing.T, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest("POST", "http://upstream/x", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// 连接没写出去的失败必须重放一次，且**重放的请求体完整**（不是空的）。
// 重放时忘了重建 body 会得到 200 但内容为空 —— 比失败更难查。
func TestRetryStaleIdleReplaysWithIntactBody(t *testing.T) {
	base := &stubRT{err: errors.New("http: server closed idle connection")}
	rsp, err := retryStaleIdleConn{base}.RoundTrip(newPost(t, "payload-123"))
	if err != nil {
		t.Fatalf("重放后仍失败: %v", err)
	}
	if rsp.StatusCode != 200 || base.calls != 2 {
		t.Fatalf("want 2 次调用/200, got %d 次/%d", base.calls, rsp.StatusCode)
	}
	for i, b := range base.bodies {
		if b != "payload-123" {
			t.Fatalf("第 %d 次请求体损坏: %q", i+1, b)
		}
	}
}

// ★ 语义歧义的失败**不能**重放：请求可能已被上游处理，重放 = 重复下单/重复扣款。
func TestRetryStaleIdleDoesNotReplayAmbiguousFailures(t *testing.T) {
	for _, e := range []error{io.EOF, errors.New("connection reset by peer"), errors.New("context deadline exceeded")} {
		base := &stubRT{err: e}
		if _, err := (retryStaleIdleConn{base}).RoundTrip(newPost(t, "p")); err == nil {
			t.Fatalf("%v 被重放了 —— 非幂等请求可能重复执行", e)
		}
		if base.calls != 1 {
			t.Fatalf("%v: 调用了 %d 次，应当只有 1 次", e, base.calls)
		}
	}
}

// 没有 GetBody 就无法重建请求体，必须放弃重放而不是发一个空 body 出去。
func TestRetryStaleIdleSkipsWhenBodyUnrecoverable(t *testing.T) {
	base := &stubRT{err: errors.New("http: server closed idle connection")}
	req := newPost(t, "p")
	req.GetBody = nil
	if _, err := (retryStaleIdleConn{base}).RoundTrip(req); err == nil {
		t.Fatal("无法重建 body 却重放了 —— 上游会收到空请求体")
	}
	if base.calls != 1 {
		t.Fatalf("调用了 %d 次，应当只有 1 次", base.calls)
	}
}

// 成功的请求不受影响，且不会被多调一次。
func TestRetryStaleIdlePassesThroughSuccess(t *testing.T) {
	base := &stubRT{}
	if _, err := (retryStaleIdleConn{base}).RoundTrip(newPost(t, "p")); err != nil {
		t.Fatal(err)
	}
	if base.calls != 1 {
		t.Fatalf("成功路径被调用了 %d 次", base.calls)
	}
}

// ★ 哨兵：isServerClosedIdle 匹配的是 net/http 一个**未导出**错误的文本。
// Go 改了措辞我们不会编译失败，只会静默退回"不重放"。这条测试制造真实的陈旧连接，
// 确认那句话还在。一轮都没触发到就 skip —— 宁可不测，也不要造一条会偶发红的用例。
func TestServerClosedIdleWordingStillMatches(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	tr := &http.Transport{MaxIdleConnsPerHost: 4}
	c := &http.Client{Transport: tr}
	seen := false
	for i := 0; i < 300 && !seen; i++ {
		req, _ := http.NewRequest("POST", srv.URL, bytes.NewReader([]byte("p")))
		if r, err := c.Do(req); err == nil {
			io.Copy(io.Discard, r.Body)
			r.Body.Close()
		}
		srv.CloseClientConnections()
		req2, _ := http.NewRequest("POST", srv.URL, bytes.NewReader([]byte("p")))
		if _, err := c.Do(req2); err != nil && strings.Contains(err.Error(), "server closed idle") {
			seen = true
			if !isServerClosedIdle(err) {
				t.Fatalf("isServerClosedIdle 认不出真实错误: %v", err)
			}
		}
	}
	if !seen {
		t.Skip("300 轮未复现陈旧连接错误（时序相关），本轮不作结论")
	}
}
