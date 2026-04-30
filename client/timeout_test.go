package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// slowServer 返回一个延迟响应的 httptest.Server
func slowServer(delay time.Duration) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
}

// withConfigTimeout 是一个 Option，直接设置 config 级别的 RequestTimeout
func withConfigTimeout(d time.Duration) Option {
	return func(cfg *Config) {
		cfg.CallOptions.RequestTimeout = d
	}
}

// TestTimeoutViaConfig 验证客户端级别 RequestTimeout 能生效
func TestTimeoutViaConfig(t *testing.T) {
	ts := slowServer(20 * time.Second)
	defer ts.Close()

	cli, err := NewHttpClient(withConfigTimeout(15 * time.Second))
	if err != nil {
		t.Fatal(err)
	}

	req, err := cli.NewRequest("GET", ts.URL+"/slow", nil)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_, err = cli.Call(req)
	elapsed := time.Since(start)

	if err == nil {
		t.Errorf("expected timeout error, got nil (elapsed %v)", elapsed)
	} else {
		t.Logf("OK: got error after %v: %v", elapsed, err)
	}
	if elapsed > 25*time.Second {
		t.Errorf("timeout not enforced: elapsed %v > 250ms", elapsed)
	}
}

// TestTimeoutSuccess 验证正常情况下（超时够长）请求能成功
func TestTimeoutSuccess(t *testing.T) {
	ts := slowServer(100 * time.Millisecond)
	defer ts.Close()

	cli, err := NewHttpClient(withConfigTimeout(2 * time.Second))
	if err != nil {
		t.Fatal(err)
	}

	req, err := cli.NewRequest("GET", ts.URL+"/slow", nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = cli.Call(req)
	if err != nil {
		t.Errorf("expected success, got: %v", err)
	}
}

// TestRequestOptionTimeout 验证 WithRequestTimeout 在 request 级别能覆盖 config 默认值
// 预期：100ms 超时生效，server 延迟 300ms 时请求失败
// 如果此测试失败（请求成功或超时 > 250ms），说明 utils.Max 逻辑导致短超时被忽略
func TestRequestOptionTimeout(t *testing.T) {
	ts := slowServer(300 * time.Millisecond)
	defer ts.Close()

	// 客户端默认 20s 超时
	cli, err := NewHttpClient()
	if err != nil {
		t.Fatal(err)
	}

	// 在请求级别设置 100ms 超时
	req, err := cli.NewRequest("GET", ts.URL+"/slow", nil, WithRequestTimeout(100*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_, err = cli.Call(req)
	elapsed := time.Since(start)

	if err == nil {
		t.Errorf("BUG: WithRequestTimeout(100ms) was IGNORED — request succeeded after %v (server delay 300ms)", elapsed)
		t.Logf("Root cause: http_client.go:414 uses utils.Max(requestTimeout, callOptsTimeout)")
		t.Logf("Max(100ms, 20s) = 20s, so the short per-request timeout is silently discarded")
	} else if elapsed > 250*time.Millisecond {
		t.Errorf("BUG: WithRequestTimeout(100ms) was IGNORED — timed out after %v instead of ~100ms", elapsed)
		t.Logf("Root cause: http_client.go:414 uses utils.Max(requestTimeout, callOptsTimeout)")
		t.Logf("Max(100ms, 20s) = 20s, so the short per-request timeout is silently discarded")
	} else {
		t.Logf("OK: WithRequestTimeout(100ms) worked, elapsed=%v", elapsed)
	}
}

// TestContextTimeout 验证通过 context.WithTimeout + WithContext 作为绕过方案
func TestContextTimeout(t *testing.T) {
	ts := slowServer(300 * time.Millisecond)
	defer ts.Close()

	cli, err := NewHttpClient()
	if err != nil {
		t.Fatal(err)
	}

	req, err := cli.NewRequest("GET", ts.URL+"/slow", nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = cli.Call(req, WithContext(ctx))
	elapsed := time.Since(start)

	if err == nil {
		t.Errorf("expected timeout error, got nil (elapsed %v)", elapsed)
	} else {
		t.Logf("OK (workaround): context timeout worked after %v: %v", elapsed, err)
	}
	if elapsed > 250*time.Millisecond {
		t.Errorf("context timeout not enforced: elapsed %v > 250ms", elapsed)
	}
}
