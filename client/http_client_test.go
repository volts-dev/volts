package client

import (
	"log"
	"net/http"
	"net/url"
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
		t.Fatal(err)
	}

	log.Println(string(resp.Body().AsBytes()))
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

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/101.0.4896.140 Safari/537.36 OPR/79.0.2135.111")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "ABC", Value: "U"})
	resp, err := client.Call(req)
	if err != nil {
		t.Fatal(err)
	}

	log.Println(resp.StatusCode, string(resp.Body().AsBytes()))
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
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/101.0.4896.140 Safari/537.36 OPR/79.0.2135.111")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Host", u.Host)
	resp, err := client.Call(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body().Close()

	for v, k := range resp.Header() {
		log.Println(v, k)
	}

	for _, k := range client.CookiesManager().Cookies(u) {
		log.Println(k.Name, k.Value)
	}

	log.Println(string(resp.Body().AsBytes()))
}
