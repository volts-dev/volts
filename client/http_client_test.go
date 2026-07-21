package client

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestGetRequest(t *testing.T) {
	client, err := NewHttpClient(
		WithPrintRequest(),
		WithJa3(
			"771,4866-4867-4865-49196-49200-159-52393-52392-52394-49195-49199-158-49188-49192-107-49187-49191-103-49162-49172-57-49161-49171-51-157-156-61-60-53-47-255,0-11-10-35-16-22-23-13-43-45-51-21,29-23-30-25-24,0-1-2",
			"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/102.0.5005.61 Safari/537.36",
		),
		WithHttpOptions(
			WithUserAgent("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/102.0.5005.61 Safari/537.36"),
		),
	)
	if err != nil {
		panic(err)
	}

	//data := []byte("你好!")
	_url := ""
	_url = "https://httpbin.org/ip" // FIXME
	//_url = "https://www.baidu.com/"// FIXME
	req, err := client.NewRequest("get", _url, nil)
	if err != nil {
		panic(err)
	}

	resp, err := client.Call(req)
	if err != nil {
		skipIfNetworkError(t, err)
	}

	log.Dbg(string(resp.Body().AsBytes()))
}

func TestPostRequest(t *testing.T) {
	client, err := NewHttpClient(
		WithPrintRequest(),
		WithJa3(
			"771,4866-4867-4865-49196-49200-159-52393-52392-52394-49195-49199-158-49188-49192-107-49187-49191-103-49162-49172-57-49161-49171-51-157-156-61-60-53-47-255,0-11-10-35-16-22-23-13-43-45-51-21,29-23-1035-25-24,0-1-2",
			"Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/101.0.4896.140 Safari/537.36 OPR/79.0.2135.111",
		),
		WithHttpOptions(
			WithUserAgent("Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/101.0.4896.140 Safari/537.36 OPR/79.0.2135.111"),
		),
	)
	if err != nil {
		panic(err)
	}

	_url := "https://go.dev/_/compile?backend="
	_context := `version=2&body=%2F%2F+You+can+edit+this+code!%0A%2F%2F+Click+here+and+start+typing.%0Apackage+main%0A%0Aimport+%22fmt%22%0A%0Afunc+main()+%7B%0A%09fmt.Println(%22Hello%2C+%E4%B8%96%E7%95%8C%22)%0A%7D%0A`
	req, err := client.NewRequest("POST", _url, _context)
	if err != nil {
		panic(err)
	}

	req.Header().Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/101.0.4896.140 Safari/537.36 OPR/79.0.2135.111")
	req.Header().Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "ABC", Value: "U"})
	resp, err := client.Call(req)
	if err != nil {
		skipIfNetworkError(t, err)
	}

	log.Dbg(resp.StatusCode(), string(resp.Body().AsBytes()))
}

func TestOrderHeader(t *testing.T) {
	//urladdr := "https://www.baidu.com/"
	urladdr := "https://www.ti.com/secure-link-forward/"
	client, err := NewHttpClient(
		WithPrintRequest(),
		WithJa3(
			"771,4866-4867-4865-49196-49200-159-52393-52392-52394-49195-49199-158-49188-49192-107-49187-49191-103-49162-49172-57-49161-49171-51-157-156-61-60-53-47-255,0-11-10-35-16-22-23-13-43-45-51-21,29-23-1035-25-24,0-1-2",
			"Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/101.0.4896.140 Safari/537.36 OPR/79.0.2135.111",
		),
		WithHttpOptions(
			WithUserAgent("Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/101.0.4896.140 Safari/537.36 OPR/79.0.2135.111"),
		),
	)
	if err != nil {
		panic(err)
	}

	req, err := client.NewRequest("get", urladdr, nil)
	if err != nil {
		panic(err)
	}
	//set our Host header
	u, err := url.Parse(urladdr)
	if err != nil {
		panic(err)
	}
	req.Header().Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/101.0.4896.140 Safari/537.36 OPR/79.0.2135.111")
	req.Header().Set("Accept", "*/*")
	req.Header().Set("Host", u.Host)
	resp, err := client.Call(req)
	if err != nil {
		skipIfNetworkError(t, err)
	}
	defer resp.Body().Reset()

	for v, k := range resp.Header() {
		log.Dbg(v, k)
	}

	for _, k := range client.CookiesManager().Cookies(u) {
		log.Dbg(k.Name, k.Value)
	}

	log.Dbg(string(resp.Body().AsBytes()))
}

// skipIfNetworkError 在网络类错误时跳过用例，其余错误照常判失败。
//
// 本文件的几个用例直接打公网（httpbin.org / go.dev / ti.com）：它们验证的是
// 真实 TLS 栈上的 JA3 指纹与请求头顺序，换成本地 httptest 就失去意义，
// 所以保留真实端点。但"外网抖动 = CI 变红"不可接受 —— 红灯一旦常态化，
// 大家就不再看红灯了。
//
// NOTE 这里刻意**不做 TCP 预探测**。本机（macOS + 网络扩展/VPN）会无差别接受
// 所有 TCP 连接：实测连 192.0.2.1（RFC 5737 保留、永不可路由）都能在 0.01s
// "连上"。任何 Dial 式的可达性探测在这种环境下恒为真，毫无意义。
// 只有真实发一次请求、再看错误类型，才分得清"外网不通"与"代码坏了"。
func skipIfNetworkError(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		return
	}

	msg := err.Error()
	for _, sig := range []string{
		"context deadline exceeded",
		"no such host",
		"connection refused",
		"connection reset",
		"i/o timeout",
		"network is unreachable",
		"TLS handshake timeout",
		"EOF",
	} {
		if strings.Contains(msg, sig) {
			t.Skipf("外网访问失败（%v）—— 本用例依赖公网端点，跳过", err)
		}
	}

	t.Fatalf("请求失败（非网络原因，需排查）: %v", err)
}
