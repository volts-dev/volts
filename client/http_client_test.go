package client

import (
	"context"
	"log"
	"net/url"
	"testing"
)

func TestOrderHeader(t *testing.T) {
	//urladdr := "https://www.baidu.com/"
	urladdr := "https://www.ti.com/secure-link-forward/"
	client := NewHttpClient(
		WithJa3(
			"771,4866-4867-4865-49196-49200-159-52393-52392-52394-49195-49199-158-49188-49192-107-49187-49191-103-49162-49172-57-49161-49171-51-157-156-61-60-53-47-255,0-11-10-35-16-22-23-13-43-45-51-21,29-23-1035-25-24,0-1-2",
			"Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/101.0.4896.140 Safari/537.36 OPR/79.0.2135.111",
		),
		WithHttpOptions(
			Ua("Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/101.0.4896.140 Safari/537.36 OPR/79.0.2135.111"),
		),
	)

	req, err := NewHttpRequest(urladdr, "get", nil)

	//set our Host header
	u, err := url.Parse(urladdr)
	if err != nil {
		panic(err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/101.0.4896.140 Safari/537.36 OPR/79.0.2135.111")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Host", u.Host)
	resp, err := client.Call(context.Background(), req)
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

	log.Println(string(resp.Body().Data.Bytes()))
}
