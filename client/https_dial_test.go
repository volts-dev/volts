package client

import (
	"bufio"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

func tlsClient(t *testing.T, opts ...Option) *HttpClient {
	t.Helper()
	opts = append(opts, func(c *Config) { c.TlsConfig = &tls.Config{InsecureSkipVerify: true} })
	cli, err := NewHttpClient(opts...)
	if err != nil {
		t.Fatal(err)
	}
	return cli
}

// 普通 HTTPS（没有配指纹）此前在 roundTripper.RoundTrip 里对一个 nil 接口调方法：
// dialTLS 只认 utls 那种自带 NegotiatedProtocol() 的连接，*tls.Conn 不实现它，
// 于是 transport 一次都没被登记。症状不是"请求失败"，是**在调用方的 goroutine 里
// panic**，谁调谁崩。
func TestHTTPS_DoesNotPanicWithoutFingerprint(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	cli := tlsClient(t)
	req, err := cli.NewRequest("GET", srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	rsp, err := cli.Call(req)
	if err != nil {
		t.Fatalf("HTTPS 请求失败: %v", err)
	}
	if got := string(rsp.Body().AsBytes()); got != `{"ok":true}` {
		t.Fatalf("响应体 = %q", got)
	}
}

// roundTripper 的两张 map 此前在 RoundTrip 里完全没上锁地判空、建表、读、写。
func TestHTTPS_Concurrent(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	cli := tlsClient(t)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := cli.NewRequest("GET", srv.URL, nil)
			if err != nil {
				errs <- err
				return
			}
			if _, err := cli.Call(req); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("并发 HTTPS 请求失败: %v", err)
	}
}

// call() 会按 config.UserAgent / Ja3.UserAgent 算出 UA 并写进请求头，
// 然后 roundTripper 无条件 Set 成自己那个从没被赋值过的空字段，把它抹掉。
// 于是 client.HttpClient 每个请求都带着空 User-Agent 出去。
func TestUserAgent_IsNotWipedByRoundTripper(t *testing.T) {
	got := make(chan string, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("User-Agent")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	cli, err := NewHttpClient(WithHttpOptions(WithUserAgent("volts-test/1.0")))
	if err != nil {
		t.Fatal(err)
	}
	req, err := cli.NewRequest("GET", srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Call(req); err != nil {
		t.Fatal(err)
	}
	if ua := <-got; ua != "volts-test/1.0" {
		t.Fatalf("服务端收到的 User-Agent = %q，想要 volts-test/1.0", ua)
	}
}

// 起一个只会做 CONNECT 的最小代理，返回它的地址和一个命中计数。
func connectProxy(t *testing.T) (string, *int64) {
	t.Helper()
	var hits int64
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				br := bufio.NewReader(c)
				req, err := http.ReadRequest(br)
				if err != nil || req.Method != http.MethodConnect {
					c.Close()
					return
				}
				up, err := net.Dial("tcp", req.Host)
				if err != nil {
					c.Close()
					return
				}
				atomic.AddInt64(&hits, 1)
				io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")
				go func() { io.Copy(up, br); up.Close() }()
				io.Copy(c, up)
				c.Close()
			}(c)
		}
	}()
	return "http://" + ln.Addr().String(), &hits
}

// 配了代理却没配指纹时，请求此前直接从本机 IP 出去：
// DialConfig.ProxyURL 没有任何读取方，而 dialer 只在 JA3 分支里被用到，
// 普通 TLS 走的是 tls.DialWithDialer —— 直连。配代理正是为了不让对端看到这个 IP。
func TestProxy_IsUsedForHTTPSWithoutFingerprint(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	proxyURL, hits := connectProxy(t)
	cli := tlsClient(t, WithProxyURL(proxyURL))
	req, err := cli.NewRequest("GET", srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Call(req); err != nil {
		t.Fatalf("经代理的 HTTPS 请求失败: %v", err)
	}
	if n := atomic.LoadInt64(hits); n == 0 {
		t.Fatal("代理一次都没被用到 —— 请求是从本机 IP 直接出去的")
	}
}
