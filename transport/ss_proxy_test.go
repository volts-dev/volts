package transport

// ss/ssr 拨号的守卫。
//
// ★ 判据是**真的跑一遍往返**，不是"解析出来的字段等于预期"。这套东西的失败方式
// 几乎全是静默的：CFB 的加解密方向搞反、AEAD 的 nonce 忘了递增、obfs 与加密层
// 的顺序颠倒 —— 每一种都能让连接顺利建立，然后两边各自读到一堆垃圾。字段断言
// 一个都抓不到，握手成功也证明不了什么。
//
// 所以下面有一个**进程内的假服务端**，它按协议真的解密、真的读地址头。客户端
// 收到自己发出去的那段话，才算这条路是通的。

import (
	"bufio"
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------- 假服务端

type fakeSsServer struct {
	t      *testing.T
	ln     net.Listener
	spec   *ssCipherSpec
	key    []byte
	obfs   string
	target chan string // 服务端读到的目标地址（客户端要访问谁）
}

func newFakeSsServer(t *testing.T, method, password, obfs string) *fakeSsServer {
	t.Helper()
	spec, err := lookupSsCipher(method)
	if err != nil {
		t.Fatalf("lookupSsCipher(%q): %v", method, err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeSsServer{
		t: t, ln: ln, spec: spec, obfs: obfs,
		key:    evpBytesToKey(password, spec.keyLen),
		target: make(chan string, 4),
	}
	go s.serve()
	t.Cleanup(func() { ln.Close() })
	return s
}

func (s *fakeSsServer) hostPort() (string, string) {
	host, port, _ := net.SplitHostPort(s.ln.Addr().String())
	return host, port
}

func (s *fakeSsServer) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(c)
	}
}

func (s *fakeSsServer) handle(raw net.Conn) {
	defer raw.Close()
	raw.SetDeadline(time.Now().Add(10 * time.Second))

	var conn net.Conn = raw
	if s.obfs == "http_simple" {
		var err error
		conn, err = serverStripHTTPSimple(raw)
		if err != nil {
			s.t.Errorf("服务端剥 http_simple 假头失败: %v", err)
			return
		}
	}

	var sc net.Conn
	if s.spec.aead {
		sc = newServerAEADConn(conn, s.key, s.spec)
	} else {
		sc = newServerStreamConn(conn, s.key, s.spec)
	}

	addr, err := readSsAddrHeader(sc)
	if err != nil {
		s.t.Errorf("服务端读地址头失败: %v", err)
		return
	}
	select {
	case s.target <- addr:
	default:
	}

	// 回声：把客户端说的每一段原样加密送回去。
	buf := make([]byte, 4096)
	for {
		n, err := sc.Read(buf)
		if n > 0 {
			if _, werr := sc.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// serverStripHTTPSimple 是 http_simple 的服务端一侧：读完假 HTTP 请求头，把 URI
// 里 %xx 编码回来的那段与请求体拼成原始字节流，并在写方向先补一个假响应头。
func serverStripHTTPSimple(raw net.Conn) (net.Conn, error) {
	br := bufio.NewReader(raw)
	var head []byte
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		head = append(head, line...)
		if bytes.HasSuffix(head, []byte("\r\n\r\n")) {
			break
		}
		if len(head) > 8192 {
			return nil, errors.New("假请求头过长")
		}
	}
	first := head[:bytes.Index(head, []byte("\r\n"))]
	parts := bytes.Split(first, []byte(" "))
	if len(parts) < 3 || !bytes.Equal(parts[0], []byte("GET")) {
		return nil, errors.New("首行不是 GET：" + string(first))
	}
	decoded, err := percentDecode(string(bytes.TrimPrefix(parts[1], []byte("/"))))
	if err != nil {
		return nil, err
	}
	return &serverObfsConn{Conn: raw, br: br, pending: decoded}, nil
}

type serverObfsConn struct {
	net.Conn
	br      *bufio.Reader
	pending []byte
	sent    bool
}

func (c *serverObfsConn) Read(b []byte) (int, error) {
	if len(c.pending) > 0 {
		n := copy(b, c.pending)
		c.pending = c.pending[n:]
		return n, nil
	}
	return c.br.Read(b)
}

func (c *serverObfsConn) Write(b []byte) (int, error) {
	if !c.sent {
		c.sent = true
		hdr := []byte("HTTP/1.1 200 OK\r\nServer: nginx\r\nContent-Type: text/html\r\n\r\n")
		if _, err := c.Conn.Write(append(hdr, b...)); err != nil {
			return 0, err
		}
		return len(b), nil
	}
	return c.Conn.Write(b)
}

func percentDecode(s string) ([]byte, error) {
	var out []byte
	for i := 0; i < len(s); {
		if s[i] != '%' || i+2 >= len(s) {
			return nil, errors.New("URI 里不是 %xx 序列")
		}
		var v byte
		for _, c := range []byte{s[i+1], s[i+2]} {
			v <<= 4
			switch {
			case c >= '0' && c <= '9':
				v |= c - '0'
			case c >= 'a' && c <= 'f':
				v |= c - 'a' + 10
			case c >= 'A' && c <= 'F':
				v |= c - 'A' + 10
			default:
				return nil, errors.New("非法十六进制")
			}
		}
		out = append(out, v)
		i += 3
	}
	return out, nil
}

// 服务端方向的加解密：与客户端**互为镜像**（客户端 Enc 的，服务端要用 Dec 读）。
func newServerStreamConn(c net.Conn, key []byte, spec *ssCipherSpec) net.Conn {
	return &serverStreamConn{Conn: c, key: key, spec: spec}
}

type serverStreamConn struct {
	net.Conn
	key  []byte
	spec *ssCipherSpec
	r    cipher.Stream
	w    cipher.Stream
}

func (c *serverStreamConn) Read(b []byte) (int, error) {
	if c.r == nil {
		iv := make([]byte, c.spec.ivLen)
		if _, err := io.ReadFull(c.Conn, iv); err != nil {
			return 0, err
		}
		s, err := c.spec.newStreamDec(c.key, iv)
		if err != nil {
			return 0, err
		}
		c.r = s
	}
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.r.XORKeyStream(b[:n], b[:n])
	}
	return n, err
}

func (c *serverStreamConn) Write(b []byte) (int, error) {
	if c.w == nil {
		iv := make([]byte, c.spec.ivLen)
		if _, err := rand.Read(iv); err != nil {
			return 0, err
		}
		s, err := c.spec.newStreamEnc(c.key, iv)
		if err != nil {
			return 0, err
		}
		c.w = s
		out := make([]byte, len(iv)+len(b))
		copy(out, iv)
		c.w.XORKeyStream(out[len(iv):], b)
		_, err = c.Conn.Write(out)
		return len(b), err
	}
	out := make([]byte, len(b))
	c.w.XORKeyStream(out, b)
	_, err := c.Conn.Write(out)
	return len(b), err
}

func newServerAEADConn(c net.Conn, key []byte, spec *ssCipherSpec) net.Conn {
	return &ssAEADConn{Conn: c, key: key, spec: spec} // AEAD 两侧完全对称
}

func readSsAddrHeader(c net.Conn) (string, error) {
	var atyp [1]byte
	if _, err := io.ReadFull(c, atyp[:]); err != nil {
		return "", err
	}
	var host string
	switch atyp[0] {
	case 1:
		b := make([]byte, 4)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		host = net.IP(b).String()
	case 3:
		var l [1]byte
		if _, err := io.ReadFull(c, l[:]); err != nil {
			return "", err
		}
		b := make([]byte, l[0])
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		host = string(b)
	case 4:
		b := make([]byte, 16)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		host = net.IP(b).String()
	default:
		return "", errors.New("未知 ATYP")
	}
	var p [2]byte
	if _, err := io.ReadFull(c, p[:]); err != nil {
		return "", err
	}
	return net.JoinHostPort(host, itoa(int(binary.BigEndian.Uint16(p[:])))), nil
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [8]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	return string(b[n:])
}

// ---------------------------------------------------------------- 往返

// TestSsRoundTrip 每一种支持的加密方式 × 每一种 obfs 都真的说一句话再听回来。
func TestSsRoundTrip(t *testing.T) {
	const password = "p@ss:word/with?specials"

	methods := make([]string, 0, len(ssCiphers))
	for name := range ssCiphers {
		methods = append(methods, name)
	}

	for _, method := range methods {
		for _, obfs := range []string{"plain", "http_simple"} {
			t.Run(method+"/"+obfs, func(t *testing.T) {
				srv := newFakeSsServer(t, method, password, obfs)
				host, port := srv.hostPort()

				raw := "ssr://" + net.JoinHostPort(host, port) +
					"?method=" + method +
					"&password=" + urlEscape(password) +
					"&protocol=origin&obfs=" + obfs
				d, err := NewProxyDialer(raw, "")
				if err != nil {
					t.Fatalf("NewProxyDialer: %v", err)
				}

				conn, err := d.(*ssDialer).DialContext(context.Background(), "tcp", "example.com:443")
				if err != nil {
					t.Fatalf("拨号失败: %v", err)
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))

				select {
				case got := <-srv.target:
					if got != "example.com:443" {
						t.Fatalf("服务端读到的目标地址 = %q，想要 example.com:443", got)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("服务端没有读到地址头（首包被吃了？）")
				}

				// 两段，故意分两次写：第一次会被 obfs 包成假 HTTP 请求，第二次是裸的。
				// 只测一次写的话，"obfs 把每一包都包了一遍"这种错抓不到。
				for _, msg := range []string{"hello volts", strings.Repeat("x", 5000)} {
					if _, err := conn.Write([]byte(msg)); err != nil {
						t.Fatalf("写失败: %v", err)
					}
					got := make([]byte, len(msg))
					if _, err := io.ReadFull(conn, got); err != nil {
						t.Fatalf("读回声失败: %v", err)
					}
					if string(got) != msg {
						t.Fatalf("回声对不上：收到 %d 字节，头 32 字节 %q", len(got), truncate(string(got), 32))
					}
				}
			})
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func urlEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.IndexByte("-_.~", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		const hex = "0123456789ABCDEF"
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0xF])
	}
	return b.String()
}

// TestSsUrlFormsAgree 三种 ss:// 写法必须解析出同一个节点。
//
// ★ 这条抓的是"从别处粘来的链接"与"我们自己生成的链接"行为不一致 —— 那种问题
// 表现为"同一个节点，手工加的能用、订阅导进来的不能用"。
func TestSsUrlFormsAgree(t *testing.T) {
	const (
		method = "aes-256-gcm"
		pass   = "sesame"
		host   = "1.2.3.4"
		port   = "8388"
	)
	userinfo := base64.RawURLEncoding.EncodeToString([]byte(method + ":" + pass))
	whole := base64.RawStdEncoding.EncodeToString([]byte(method + ":" + pass + "@" + host + ":" + port))

	for _, raw := range []string{
		method + ":" + pass + "@" + host + ":" + port,
		userinfo + "@" + host + ":" + port,
		userinfo + "@" + host + ":" + port + "/?plugin=obfs-local#香港01",
		whole + "#香港01",
	} {
		p, err := parseSsURL(raw)
		if err != nil {
			t.Fatalf("parseSsURL(%q): %v", raw, err)
		}
		if p.Host != host || p.Port != port || p.Method != method || p.Password != pass {
			t.Fatalf("parseSsURL(%q) = %+v", raw, p)
		}
	}
}

// TestParseSsrStandardURL 标准 ssr 订阅串的**段序**。
//
// ★ 这条是有来历的：host:port:protocol:method:obfs:password 六段，写反 method 与
// obfs 之后，报错是"加密方式 http_simple 不支持" —— 一句指向 obfs 的话，而错的是
// 段序。真节点上更糟：两个都合法时它会拿 obfs 当加密方式去握手，然后静静地不通。
func TestParseSsrStandardURL(t *testing.T) {
	pwd := base64.RawURLEncoding.EncodeToString([]byte("mypass"))
	obfsParam := base64.RawURLEncoding.EncodeToString([]byte("cloud.example.com"))
	remarks := base64.RawURLEncoding.EncodeToString([]byte("🇭🇰 HK01"))
	body := "9.8.7.6:12345:origin:chacha20-ietf:http_simple:" + pwd +
		"/?obfsparam=" + obfsParam + "&remarks=" + remarks
	raw := base64.RawURLEncoding.EncodeToString([]byte(body))

	p, err := parseSsrURL(raw)
	if err != nil {
		t.Fatalf("parseSsrURL: %v", err)
	}
	if p.Host != "9.8.7.6" || p.Port != "12345" {
		t.Fatalf("host/port = %s:%s", p.Host, p.Port)
	}
	if p.Protocol != "origin" || p.Method != "chacha20-ietf" || p.Obfs != "http_simple" {
		t.Fatalf("protocol/method/obfs = %s/%s/%s（段序错了）", p.Protocol, p.Method, p.Obfs)
	}
	if p.Password != "mypass" || p.ObfsParam != "cloud.example.com" || p.Tag != "🇭🇰 HK01" {
		t.Fatalf("password/obfsparam/remarks = %q/%q/%q", p.Password, p.ObfsParam, p.Tag)
	}
}

// TestUnsupportedPluginsAreNamed 实现不了的协议/混淆必须**报错并点名**。
//
// 静默地当成 origin 试一把的后果是：连接建得起来，每个包服务端都解不开就丢，
// 表现是卡到超时 —— 日志里没有任何东西指向"这个节点用了 auth_chain_a"。
func TestUnsupportedPluginsAreNamed(t *testing.T) {
	cases := []struct{ url, want string }{
		{"ssr://1.2.3.4:80?method=aes-256-cfb&password=x&protocol=auth_aes128_md5", "auth_aes128_md5"},
		{"ssr://1.2.3.4:80?method=aes-256-cfb&password=x&obfs=tls1.2_ticket_auth", "tls1.2_ticket_auth"},
		{"ss://rc4-md5:x@1.2.3.4:80", "rc4-md5"},
		{"vmess://whatever", "vmess"},
		{"hysteria2://whatever", "hysteria2"},
	}
	for _, c := range cases {
		_, err := NewProxyDialer(c.url, "")
		if err == nil {
			t.Fatalf("%s 应当报错", c.url)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s 的错误里没有点名 %q：%v", c.url, c.want, err)
		}
	}
}

// TestSsDialerIsContextDialer —— core/outbound 的 dialRaw 靠这个接口把 ctx 传下去。
// 不实现它也能跑（有退路），但那次拨号就不受 ctx 管了，一个吞包的代理能耗光整个
// 请求预算。
func TestSsDialerIsContextDialer(t *testing.T) {
	d, err := NewProxyDialer("ss://aes-256-gcm:x@1.2.3.4:8388", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := d.(interface {
		DialContext(context.Context, string, string) (net.Conn, error)
	}); !ok {
		t.Fatal("ssDialer 没有实现 proxy.ContextDialer")
	}
}

// TestSsDialRejectsUDP —— outbound 只发 HTTP，但接口上 network 是调用方给的。
func TestSsDialRejectsUDP(t *testing.T) {
	d, _ := NewProxyDialer("ss://aes-256-gcm:x@1.2.3.4:8388", "")
	if _, err := d.Dial("udp", "example.com:53"); err == nil {
		t.Fatal("udp 应当被拒绝")
	}
}
