package transport

import (
	"context"
	"encoding/base64"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/net/proxy"
)

// TestNewProxyDialerSocks5 —— socks5 曾经是这个 switch 里的一个**空 case 块**。
//
// 后果不是"不支持"，是更难查的那一种：URL 被 NewProxyDialer 欣然接受，回来一个
// HTTP CONNECT 拨号器，然后**每一次拨号**都失败在一句 "scheme socks5 is not
// supported" 上。调用方看着自己写的 socks5:// 被接受了，只能去怀疑代理服务器。
func TestNewProxyDialerSocks5(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// 一个只认握手的极简 SOCKS5：回"无需认证"，再对 CONNECT 回一句成功。
	// 够用来证明拨号器真的说的是 SOCKS5 而不是 HTTP CONNECT。
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		buf := make([]byte, 512)
		if _, err := conn.Read(buf); err != nil { // VER NMETHODS METHODS
			return
		}
		conn.Write([]byte{0x05, 0x00}) // 选"无需认证"

		if _, err := conn.Read(buf); err != nil { // VER CMD RSV ATYP ADDR PORT
			return
		}
		// 回 succeeded + 绑定地址 0.0.0.0:0
		conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		io.Copy(io.Discard, conn)
	}()

	dialer, err := NewProxyDialer("socks5://"+ln.Addr().String(), "test-agent")
	if err != nil {
		t.Fatalf("socks5 URL 被拒绝了: %v", err)
	}

	// 必须支持 ContextDialer，否则调用方的超时对代理拨号完全无效。
	cd, ok := dialer.(proxy.ContextDialer)
	if !ok {
		t.Fatal("socks5 拨号器不是 ContextDialer")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := cd.DialContext(ctx, "tcp", "example.com:443")
	if err != nil {
		t.Fatalf("经 socks5 拨号失败: %v", err)
	}
	conn.Close()
}

// socks5 不带端口时补 1080（SOCKS 的标准端口），不是 CONNECT 那条路的 80/443。
func TestNewProxyDialerSocks5DefaultPort(t *testing.T) {
	for _, scheme := range []string{"socks5", "socks5h"} {
		if _, err := NewProxyDialer(scheme+"://127.0.0.1", ""); err != nil {
			t.Errorf("%s:// 无端口被拒绝: %v", scheme, err)
		}
	}
}

// 代理 URL 里的用户名密码要落到 CONNECT 的 Proxy-Authorization 上 ——
// 不然一个需要认证的 HTTP 代理会稳定回 407，而错误信息里不会提到凭据。
func TestNewProxyDialerHTTPCarriesCredentials(t *testing.T) {
	dialer, err := NewProxyDialer("http://alice:s3cr3t@127.0.0.1:8080", "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	cd, ok := dialer.(*connectDialer)
	if !ok {
		t.Fatalf("http 代理拿到的是 %T", dialer)
	}

	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:s3cr3t"))
	if got := cd.DefaultHeader.Get(proxyAuthHeader); got != want {
		t.Errorf("%s = %q, 想要 %q", proxyAuthHeader, got, want)
	}
	if got := cd.DefaultHeader.Get("User-Agent"); got != "test-agent" {
		t.Errorf("User-Agent = %q", got)
	}
}

func TestNewProxyDialerRejectsUnknownScheme(t *testing.T) {
	for _, raw := range []string{"ftp://host:21", "host:1080", ""} {
		if _, err := NewProxyDialer(raw, ""); err == nil {
			t.Errorf("%q 应该被拒绝", raw)
		}
	}
}
