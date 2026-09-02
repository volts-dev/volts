package transport

// ss_proxy.go —— Shadowsocks / ShadowsocksR 出口的拨号实现。
//
// 存在的理由：`core/outbound` 的身份轮换里，**唯一能换掉出口 IP 的那一半是代理**
// （指纹和 UA 换多少次，对方按 IP 限速时都没有效果）。而中文区能买到的代理绝大
// 多数是"机场订阅"，订阅里的节点是 ss/ssr —— 不是 http，也不是 socks5。只支持
// http/socks5 的话，"支持 clash 订阅"这个功能导进来一屏节点、可用数是 0。
//
// # 为什么在主模块里，而不是像 core/ja3 那样做成子模块
//
// core/ja3 独立成子模块的判据是**依赖**：utls 是一个"模仿别的客户端指纹"的库，
// 会被无条件编进每个 volts 二进制并在合规扫描里被点名。本文件不引入任何新依赖
// （只用 x/crypto 的 chacha20/hkdf，volts 本来就直接依赖 x/crypto），所以那条
// 判据在这里不成立。放在 transport 里换来的是：`NewProxyDialer` 仍然是**唯一**
// 入口，调用方给一个 URL 字符串就行，不必知道有几种协议、也不会因为忘了空导入
// 而得到一个"看起来在用代理、其实是直连"的静默降级。
//
// # 支持到哪一步
//
//	ss://    AEAD（aes-128/192/256-gcm、chacha20-ietf-poly1305）与流加密
//	ssr://   protocol=origin，obfs=plain / http_simple
//
// **不支持**：ssr 的 auth_aes128_md5 / auth_aes128_sha1 / auth_chain_* 协议插件、
// tls1.2_ticket_auth 等 obfs、以及 vmess / vless / trojan / hysteria2 / tuic。
// 遇到它们**报错并说出名字**，不做"跳过一个节点"式的静默降级 —— 一个悄悄少了
// 一半出口的池子，表现只是"还是被挡"，没人查得到原因。
//
// 这条边界不是偷懒画的：origin + plain/http_simple 的 ssr 本质上就是"经典
// shadowsocks 流加密 + 一层 HTTP 头伪装"，而 auth_chain 那一族是另一套带状态的
// 协议机器，hysteria2/tuic 则要一整个 QUIC 栈。
//
// # 三层的顺序（写错就是握手成功但一个字节也解不出来）
//
//	应用数据 ──▶ [SS 加密层] ──▶ [SSR obfs 层] ──▶ TCP
//
// obfs 包的是**密文**（连同最前面那段 IV/salt 一起）。反过来先 obfs 再加密的话，
// 假 HTTP 头会被一起加密，服务端在 obfs 那一层看到的是随机字节 —— 连接建得起来，
// 然后静静地什么都读不到。

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	mathrand "math/rand"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/chacha20"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
	"golang.org/x/net/proxy"
)

// ssMaxPayload 是 AEAD 分片的上限，SIP004 规定的 0x3FFF。
const ssMaxPayload = 0x3FFF

// ssDialTimeout 连到代理服务器本身的期限。目标地址那一跳的期限由调用方的 ctx 管。
const ssDialTimeout = 10 * time.Second

// ssParams 一个 ss/ssr 出口的全部参数。
type ssParams struct {
	Scheme   string // "ss" / "ssr"
	Host     string
	Port     string
	Method   string
	Password string

	// 以下只对 ssr 有意义
	Protocol      string
	ProtocolParam string
	Obfs          string
	ObfsParam     string

	Tag string // 节点名（#后面那段），只用于日志
}

func (p *ssParams) address() string { return net.JoinHostPort(p.Host, p.Port) }

// newShadowsocksDialer 由 NewProxyDialer 调用，rest 是 "://" 之后的部分。
func newShadowsocksDialer(scheme, rest string) (proxy.Dialer, error) {
	var (
		p   *ssParams
		err error
	)
	switch scheme {
	case "ss":
		p, err = parseSsURL(rest)
	case "ssr":
		p, err = parseSsrURL(rest)
	default:
		return nil, errors.New("scheme " + scheme + " is not supported")
	}
	if err != nil {
		return nil, err
	}

	spec, err := lookupSsCipher(p.Method)
	if err != nil {
		return nil, err
	}
	if err := checkSsrPlugins(p); err != nil {
		return nil, err
	}

	return &ssDialer{
		p:    p,
		spec: spec,
		key:  evpBytesToKey(p.Password, spec.keyLen),
		fwd:  &net.Dialer{Timeout: ssDialTimeout},
	}, nil
}

// checkSsrPlugins 挡下我们实现不了的 ssr 协议/混淆插件。
//
// ★ 必须报错而不是"当成 origin 试试"：协议插件改的是**每一个包**的形状，当成
// origin 发过去，服务端解不开就是静默丢弃 —— 表现为连接建立后卡到超时，日志里
// 没有任何东西指向这里。
func checkSsrPlugins(p *ssParams) error {
	switch p.Protocol {
	case "", "origin":
	default:
		return fmt.Errorf("ssr 协议插件 %q 不支持（本实现只支持 origin）；"+
			"auth_aes128_* / auth_chain_* 是另一套带状态的协议机器，没有实现", p.Protocol)
	}
	switch p.Obfs {
	case "", "plain", "http_simple":
	default:
		return fmt.Errorf("ssr 混淆 %q 不支持（本实现只支持 plain 与 http_simple）", p.Obfs)
	}
	return nil
}

// ---------------------------------------------------------------- URL 解析

// parseSsURL 认三种写法（都真实存在于订阅与二维码里）：
//
//	ss://method:password@host:port                   明文（本仓生成的就是这种）
//	ss://base64url(method:password)@host:port        SIP002
//	ss://base64(method:password@host:port)           旧式整包 base64
//
// ★ 明文那种的口令要 URL 转义。不转的话，一个带 `@` 或 `/` 的口令会把 host 解析
// 到别处去 —— 而错误信息说的是"连不上某个不存在的主机"，与口令毫无关系。
func parseSsURL(rest string) (*ssParams, error) {
	body, tag := cutTag(rest)
	if body == "" {
		return nil, errors.New("ss:// 后面是空的")
	}

	if at := strings.LastIndex(body, "@"); at >= 0 {
		userInfo, hostPort := body[:at], body[at+1:]
		// SIP002：userinfo 整段是 base64（没有冒号）。明文写法里有冒号。
		method, password := "", ""
		if strings.Contains(userInfo, ":") {
			u, err := url.Parse("ss://" + body)
			if err != nil || u.User == nil {
				return nil, fmt.Errorf("ss:// 解析失败: %v", err)
			}
			method = u.User.Username()
			password, _ = u.User.Password()
			hostPort = u.Host
		} else {
			raw, err := b64decode(userInfo)
			if err != nil {
				return nil, fmt.Errorf("ss:// 的 userinfo 既不是 method:password，也不是合法 base64: %v", err)
			}
			method, password, _ = strings.Cut(string(raw), ":")
		}
		// 去掉 SIP002 可能带的 "/?plugin=..." 尾巴：插件（v2ray-plugin 等）我们不支持，
		// 但不该因为 URL 上挂了个尾巴就连解析都失败 —— 下面的 lookupSsCipher 会
		// 因为别的原因报错时，人至少看得懂。
		if i := strings.IndexAny(hostPort, "/?"); i >= 0 {
			hostPort = hostPort[:i]
		}
		host, port, err := splitHostPort(hostPort)
		if err != nil {
			return nil, err
		}
		return &ssParams{Scheme: "ss", Host: host, Port: port,
			Method: method, Password: password, Tag: tag}, nil
	}

	// 旧式：整包 base64
	raw, err := b64decode(body)
	if err != nil {
		return nil, fmt.Errorf("ss:// 既不含 @，也不是合法 base64: %v", err)
	}
	inner := string(raw)
	at := strings.LastIndex(inner, "@")
	if at < 0 {
		return nil, errors.New("ss:// 解码后不是 method:password@host:port")
	}
	method, password, _ := strings.Cut(inner[:at], ":")
	host, port, err := splitHostPort(inner[at+1:])
	if err != nil {
		return nil, err
	}
	return &ssParams{Scheme: "ss", Host: host, Port: port,
		Method: method, Password: password, Tag: tag}, nil
}

// parseSsrURL 认两种写法：
//
//	ssr://host:port?method=..&password=..&protocol=origin&obfs=http_simple&obfs_param=..
//	ssr://base64url(host:port:protocol:method:obfs:base64url(password)/?obfsparam=..&protoparam=..)
//
// 第一种是本仓自己生成的（界面上配的节点转成 URL 时用它）：**能一眼看懂**，出问题
// 时日志和报错里能直接读出是哪个参数不对；第二种是订阅/二维码里的标准形式，从别处
// 粘过来的都长这样。
func parseSsrURL(rest string) (*ssParams, error) {
	if rest == "" {
		return nil, errors.New("ssr:// 后面是空的")
	}

	// 明文形式：必须带 ?method=
	if strings.Contains(rest, "?") {
		u, err := url.Parse("ssr://" + rest)
		if err == nil && u.Query().Get("method") != "" {
			host, port, err := splitHostPort(u.Host)
			if err != nil {
				return nil, err
			}
			q := u.Query()
			return &ssParams{
				Scheme: "ssr", Host: host, Port: port,
				Method:        q.Get("method"),
				Password:      q.Get("password"),
				Protocol:      firstNonEmpty(q.Get("protocol"), "origin"),
				ProtocolParam: q.Get("protocol_param"),
				Obfs:          firstNonEmpty(q.Get("obfs"), "plain"),
				ObfsParam:     q.Get("obfs_param"),
				Tag:           q.Get("remarks"),
			}, nil
		}
	}

	body, _ := cutTag(rest)
	raw, err := b64decode(body)
	if err != nil {
		return nil, fmt.Errorf("ssr:// 既不是 host:port?method=… 的明文形式，也不是合法 base64: %v", err)
	}

	main, query, _ := strings.Cut(string(raw), "/?")
	// host:port:protocol:method:obfs:base64(password)
	parts := strings.Split(main, ":")
	if len(parts) < 6 {
		return nil, fmt.Errorf("ssr:// 解码后不是 host:port:protocol:method:obfs:password，得到 %d 段", len(parts))
	}
	// host 里可能是 IPv6（含冒号）—— 标准 ssr 串不支持，这里按"后 5 段固定"来切，
	// 前面剩下的全部算 host，于是 IPv6 也能过。
	n := len(parts)
	host := strings.Join(parts[:n-5], ":")
	pwdRaw, err := b64decode(parts[n-1])
	if err != nil {
		return nil, fmt.Errorf("ssr:// 的口令不是合法 base64: %v", err)
	}
	// host:port:protocol:method:obfs:base64(password) —— 按**从后往前**数，
	// 这样 host 里带冒号（IPv6）也切得对。
	p := &ssParams{
		Scheme:   "ssr",
		Host:     host,
		Port:     parts[n-5],
		Protocol: parts[n-4],
		Method:   parts[n-3],
		Obfs:     parts[n-2],
		Password: string(pwdRaw),
	}

	if _, err := strconv.Atoi(p.Port); err != nil {
		return nil, fmt.Errorf("ssr:// 的端口 %q 不是数字", p.Port)
	}

	for k, v := range parseQuery(query) {
		switch k {
		case "obfsparam":
			if d, err := b64decode(v); err == nil {
				p.ObfsParam = string(d)
			}
		case "protoparam":
			if d, err := b64decode(v); err == nil {
				p.ProtocolParam = string(d)
			}
		case "remarks":
			if d, err := b64decode(v); err == nil {
				p.Tag = string(d)
			}
		}
	}
	return p, nil
}

// parseQuery 解 ssr 串尾巴上的 a=b&c=d（值是 base64，不能用 url.ParseQuery：
// base64url 里的 `-`/`_` 没问题，但标准 base64 的 `+` 会被 ParseQuery 解成空格）。
func parseQuery(q string) map[string]string {
	out := map[string]string{}
	for _, kv := range strings.Split(q, "&") {
		if kv == "" {
			continue
		}
		k, v, _ := strings.Cut(kv, "=")
		out[strings.ToLower(k)] = v
	}
	return out
}

func cutTag(s string) (body, tag string) {
	body, tag, _ = strings.Cut(s, "#")
	if unescaped, err := url.QueryUnescape(tag); err == nil {
		tag = unescaped
	}
	return body, tag
}

func splitHostPort(hostPort string) (host, port string, err error) {
	host, port, err = net.SplitHostPort(hostPort)
	if err != nil {
		return "", "", fmt.Errorf("%q 不是 host:port: %v", hostPort, err)
	}
	if _, e := strconv.Atoi(port); e != nil {
		return "", "", fmt.Errorf("端口 %q 不是数字", port)
	}
	return host, port, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// b64decode 容忍四种 base64 变体。订阅源里四种都见得到，而"少了两个等号"这种
// 差别不该变成"这个节点用不了"。
func b64decode(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{
		base64.RawURLEncoding, base64.URLEncoding,
		base64.RawStdEncoding, base64.StdEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("不是合法的 base64")
}

// ---------------------------------------------------------------- 加密套件

type ssCipherSpec struct {
	name   string
	keyLen int
	ivLen  int // 流加密是 IV；AEAD 是 salt（长度等于 key）
	aead   bool

	// ★ 加密与解密**分开两个构造器**。CTR 与 chacha20 是对称的（同一个流两边都能
	// 用），**CFB 不是** —— 它的反馈用的是密文，写方向必须 NewCFBEncrypter、读方向
	// 必须 NewCFBDecrypter。两边共用一个的话握手照样"成功"，然后每一个字节都是错的。
	newStreamEnc func(key, iv []byte) (cipher.Stream, error)
	newStreamDec func(key, iv []byte) (cipher.Stream, error)
	newAEAD      func(subkey []byte) (cipher.AEAD, error)
}

func lookupSsCipher(name string) (*ssCipherSpec, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return nil, errors.New("没有指定加密方式（method）")
	}
	if s, ok := ssCiphers[n]; ok {
		return s, nil
	}
	return nil, fmt.Errorf("加密方式 %q 不支持；支持的有 %s", name, strings.Join(ssCipherNames(), ", "))
}

func ssCipherNames() []string {
	out := make([]string, 0, len(ssCiphers))
	for k := range ssCiphers {
		out = append(out, k)
	}
	slices.Sort(out) // 给人看的错误信息，顺序要稳定
	return out
}

func aesCFB(keyLen int) *ssCipherSpec {
	mk := func(f func(cipher.Block, []byte) cipher.Stream) func(key, iv []byte) (cipher.Stream, error) {
		return func(key, iv []byte) (cipher.Stream, error) {
			blk, err := aes.NewCipher(key)
			if err != nil {
				return nil, err
			}
			return f(blk, iv), nil
		}
	}
	return &ssCipherSpec{
		keyLen: keyLen, ivLen: aes.BlockSize,
		newStreamEnc: mk(cipher.NewCFBEncrypter),
		newStreamDec: mk(cipher.NewCFBDecrypter),
	}
}

func aesCTR(keyLen int) *ssCipherSpec {
	mk := func(key, iv []byte) (cipher.Stream, error) {
		blk, err := aes.NewCipher(key)
		if err != nil {
			return nil, err
		}
		return cipher.NewCTR(blk, iv), nil
	}
	// CTR 是对称的：两个方向同一个构造器。
	return &ssCipherSpec{keyLen: keyLen, ivLen: aes.BlockSize, newStreamEnc: mk, newStreamDec: mk}
}

func aesGCM(keyLen int) *ssCipherSpec {
	return &ssCipherSpec{
		keyLen: keyLen, ivLen: keyLen, aead: true,
		newAEAD: func(subkey []byte) (cipher.AEAD, error) {
			blk, err := aes.NewCipher(subkey)
			if err != nil {
				return nil, err
			}
			return cipher.NewGCM(blk)
		},
	}
}

// ssCiphers 支持的加密方式表。
//
// ★ chacha20-ietf（12 字节 IV）在，**原版 chacha20（8 字节 nonce）不在** ——
// x/crypto/chacha20 只接受 12/24 字节的 nonce，而"用 12 字节的实现去跑 8 字节的
// 协议"会安静地算出一串对不上的密钥流：连接建得起来，数据全是垃圾。宁可在
// lookupSsCipher 那里明确说不支持。
var ssCiphers = map[string]*ssCipherSpec{
	"aes-128-cfb":   aesCFB(16),
	"aes-192-cfb":   aesCFB(24),
	"aes-256-cfb":   aesCFB(32),
	"aes-128-ctr":   aesCTR(16),
	"aes-192-ctr":   aesCTR(24),
	"aes-256-ctr":   aesCTR(32),
	"chacha20-ietf": chacha20Stream(),
	"aes-128-gcm":   aesGCM(16),
	"aes-192-gcm":   aesGCM(24),
	"aes-256-gcm":   aesGCM(32),
	"chacha20-ietf-poly1305": {
		keyLen: 32, ivLen: 32, aead: true,
		newAEAD: func(subkey []byte) (cipher.AEAD, error) { return chacha20poly1305.New(subkey) },
	},
	"chacha20-poly1305": {
		keyLen: 32, ivLen: 32, aead: true,
		newAEAD: func(subkey []byte) (cipher.AEAD, error) { return chacha20poly1305.New(subkey) },
	},
	"xchacha20-ietf-poly1305": {
		keyLen: 32, ivLen: 32, aead: true,
		newAEAD: func(subkey []byte) (cipher.AEAD, error) { return chacha20poly1305.NewX(subkey) },
	},
}

// chacha20Stream —— chacha20-ietf（12 字节 IV），流本身对称。
func chacha20Stream() *ssCipherSpec {
	mk := func(key, iv []byte) (cipher.Stream, error) {
		return chacha20.NewUnauthenticatedCipher(key, iv)
	}
	return &ssCipherSpec{keyLen: 32, ivLen: 12, newStreamEnc: mk, newStreamDec: mk}
}

func init() {
	for name, spec := range ssCiphers {
		spec.name = name
	}
}

// evpBytesToKey 是 shadowsocks 的口令派生：反复 MD5(上一段 || 口令) 直到够长。
//
// 弱是弱（没有盐、没有迭代次数），但它是协议的一部分 —— 换成别的 KDF 就换出了一个
// 服务端解不开的客户端。
func evpBytesToKey(password string, keyLen int) []byte {
	var (
		out  = make([]byte, 0, keyLen+md5.Size)
		prev []byte
	)
	for len(out) < keyLen {
		h := md5.New()
		h.Write(prev)
		h.Write([]byte(password))
		prev = h.Sum(nil)
		out = append(out, prev...)
	}
	return out[:keyLen]
}

// ---------------------------------------------------------------- 拨号器

type ssDialer struct {
	p    *ssParams
	spec *ssCipherSpec
	key  []byte
	fwd  *net.Dialer
}

func (d *ssDialer) Dial(network, addr string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, addr)
}

func (d *ssDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		// UDP over ss 要的是另一套（无连接、每包带地址头），而 outbound 只发 HTTP。
		return nil, fmt.Errorf("%s 代理只支持 TCP，收到 %q", d.p.Scheme, network)
	}

	target, err := ssAddrHeader(addr)
	if err != nil {
		return nil, err
	}

	raw, err := d.fwd.DialContext(ctx, "tcp", d.p.address())
	if err != nil {
		return nil, err
	}

	// ctx 取消时把连接带走 —— 否则一次被取消的拨号会留下一条谁也不认识的 TCP 连接。
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			raw.SetDeadline(time.Now()) // 让下面阻塞中的 Write 立刻回错
		case <-done:
		}
	}()

	var conn net.Conn = raw
	if d.p.Obfs == "http_simple" {
		conn = newHTTPSimpleConn(conn, d.p)
	}
	if d.spec.aead {
		conn = newSsAEADConn(conn, d.key, d.spec)
	} else {
		conn = newSsStreamConn(conn, d.key, d.spec)
	}

	// 目标地址头**立刻发**，而不是等上层第一次 Write 时捎带。
	//
	// ★ 捎带看着更省一个包，代价是"服务端先说话"的协议（比如某些代理链）会双方
	// 一起等到超时。而这里省下的那个包，在 http_simple 那条路上本来也会被合进
	// 假 HTTP 请求里。
	if _, err := conn.Write(target); err != nil {
		raw.Close()
		return nil, fmt.Errorf("%s 代理 %s 握手失败: %w", d.p.Scheme, d.p.address(), err)
	}
	if err := raw.SetDeadline(time.Time{}); err != nil {
		raw.Close()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		raw.Close()
		return nil, err
	}
	return conn, nil
}

// ssAddrHeader 把目标地址编成 shadowsocks 的地址头：ATYP | 地址 | 端口（大端）。
//
// ★ 域名**不在本地解析**（ATYP=3 原样发过去，由代理服务器解析）。本地解析等于把
// "我要访问谁"用明文 DNS 广播出去 —— 走代理却让 DNS 泄露，等于没走。
func ssAddrHeader(addr string) ([]byte, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("目标地址 %q 不是 host:port: %v", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 0 || port > 65535 {
		return nil, fmt.Errorf("目标端口 %q 非法", portStr)
	}

	var buf []byte
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			buf = append([]byte{1}, v4...)
		} else {
			buf = append([]byte{4}, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return nil, fmt.Errorf("域名太长（%d 字节，上限 255）", len(host))
		}
		buf = append([]byte{3, byte(len(host))}, host...)
	}
	return binary.BigEndian.AppendUint16(buf, uint16(port)), nil
}

// ---------------------------------------------------------------- 流加密连接

type ssStreamConn struct {
	net.Conn
	key  []byte
	spec *ssCipherSpec

	w cipher.Stream
	r cipher.Stream
}

func newSsStreamConn(c net.Conn, key []byte, spec *ssCipherSpec) net.Conn {
	return &ssStreamConn{Conn: c, key: key, spec: spec}
}

func (c *ssStreamConn) Write(b []byte) (int, error) {
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
		// IV 与首段密文必须**一次写出去**：分两次 Write 的话，http_simple 会把
		// 第一次（只有 IV）当成整个首包编进假 HTTP 请求，剩下的裸密文跟在后面 ——
		// 服务端剥完头拿到的是半段数据。
		out := make([]byte, len(iv)+len(b))
		copy(out, iv)
		c.w.XORKeyStream(out[len(iv):], b)
		if _, err := c.Conn.Write(out); err != nil {
			return 0, err
		}
		return len(b), nil
	}
	out := make([]byte, len(b))
	c.w.XORKeyStream(out, b)
	if _, err := c.Conn.Write(out); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *ssStreamConn) Read(b []byte) (int, error) {
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

// ---------------------------------------------------------------- AEAD 连接

type ssAEADConn struct {
	net.Conn
	key  []byte
	spec *ssCipherSpec

	w       cipher.AEAD
	r       cipher.AEAD
	wNonce  []byte
	rNonce  []byte
	pending []byte // 已解密但上层还没读走的明文
}

func newSsAEADConn(c net.Conn, key []byte, spec *ssCipherSpec) net.Conn {
	return &ssAEADConn{Conn: c, key: key, spec: spec}
}

// ssSubkey 是 SIP004 的会话密钥：HKDF-SHA1(主密钥, salt, "ss-subkey")。
func ssSubkey(key, salt []byte, keyLen int) ([]byte, error) {
	out := make([]byte, keyLen)
	if _, err := io.ReadFull(hkdf.New(sha1.New, key, salt, []byte("ss-subkey")), out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *ssAEADConn) Write(b []byte) (int, error) {
	var prefix []byte
	if c.w == nil {
		salt := make([]byte, c.spec.ivLen)
		if _, err := rand.Read(salt); err != nil {
			return 0, err
		}
		subkey, err := ssSubkey(c.key, salt, c.spec.keyLen)
		if err != nil {
			return 0, err
		}
		aead, err := c.spec.newAEAD(subkey)
		if err != nil {
			return 0, err
		}
		c.w = aead
		c.wNonce = make([]byte, aead.NonceSize())
		prefix = salt
	}

	// 与流加密同理：salt 与首个分片一次写出去。
	total := len(b)
	out := make([]byte, 0, len(prefix)+len(b)+64)
	out = append(out, prefix...)
	for len(b) > 0 {
		n := min(len(b), ssMaxPayload)
		var hdr [2]byte
		binary.BigEndian.PutUint16(hdr[:], uint16(n))
		out = c.w.Seal(out, c.wNonce, hdr[:], nil)
		ssIncNonce(c.wNonce)
		out = c.w.Seal(out, c.wNonce, b[:n], nil)
		ssIncNonce(c.wNonce)
		b = b[n:]
	}
	if _, err := c.Conn.Write(out); err != nil {
		return 0, err
	}
	return total, nil
}

func (c *ssAEADConn) Read(b []byte) (int, error) {
	if len(c.pending) > 0 {
		n := copy(b, c.pending)
		c.pending = c.pending[n:]
		return n, nil
	}
	if c.r == nil {
		salt := make([]byte, c.spec.ivLen)
		if _, err := io.ReadFull(c.Conn, salt); err != nil {
			return 0, err
		}
		subkey, err := ssSubkey(c.key, salt, c.spec.keyLen)
		if err != nil {
			return 0, err
		}
		aead, err := c.spec.newAEAD(subkey)
		if err != nil {
			return 0, err
		}
		c.r = aead
		c.rNonce = make([]byte, aead.NonceSize())
	}

	tag := c.r.Overhead()
	hdr := make([]byte, 2+tag)
	if _, err := io.ReadFull(c.Conn, hdr); err != nil {
		return 0, err
	}
	lenBuf, err := c.r.Open(nil, c.rNonce, hdr, nil)
	if err != nil {
		return 0, fmt.Errorf("ss: 分片长度解密失败（口令或加密方式不对？）: %w", err)
	}
	ssIncNonce(c.rNonce)

	size := int(binary.BigEndian.Uint16(lenBuf))
	if size > ssMaxPayload {
		// 长度是**对方给的**，而它下一步就是 make([]byte, size)。上限来自协议本身。
		return 0, fmt.Errorf("ss: 分片长度 %d 超过协议上限 %d", size, ssMaxPayload)
	}
	body := make([]byte, size+tag)
	if _, err := io.ReadFull(c.Conn, body); err != nil {
		return 0, err
	}
	plain, err := c.r.Open(body[:0], c.rNonce, body, nil)
	if err != nil {
		return 0, fmt.Errorf("ss: 分片解密失败: %w", err)
	}
	ssIncNonce(c.rNonce)

	n := copy(b, plain)
	if n < len(plain) {
		c.pending = append(c.pending[:0], plain[n:]...)
	}
	return n, nil
}

// ssIncNonce 小端 +1。SIP004 就是这么定的。
func ssIncNonce(n []byte) {
	for i := range n {
		n[i]++
		if n[i] != 0 {
			return
		}
	}
}

// ------------------------------------------------------- SSR http_simple obfs

// httpSimpleConn 把**首包**伪装成一个普通的 HTTP GET，并在读方向剥掉对方回的那个
// 假响应头。之后两个方向都原样透传。
//
// 为什么只有首包：中间设备判断"这是不是 HTTP"看的就是连接开头那几十字节，后面
// 再包装除了浪费带宽没有任何用 —— 原版 shadowsocksr 的 http_simple 就是这样。
type httpSimpleConn struct {
	net.Conn
	host string // Host 头（obfs_param 优先，否则用服务器地址）
	port string

	sent     bool
	recvDone bool
	hdrBuf   []byte // 还没找到 \r\n\r\n 的累积
	pending  []byte // 剥完头之后多读到的那部分
}

// httpSimpleMaxHeader 是读方向上等待 \r\n\r\n 的上限。
//
// ★ 这个上限是**必须的**：对面是不是一个正常的 ssr 服务端由不得我们，一个只发
// 数据不发 \r\n\r\n 的对端会让 hdrBuf 一直长下去 —— 同 websocket 那条读上限，
// 是内存放大而不是"格式不对"。
const httpSimpleMaxHeader = 8192

func newHTTPSimpleConn(c net.Conn, p *ssParams) net.Conn {
	host := p.Host
	if p.ObfsParam != "" {
		// obfs_param 可以是 "a.com,b.com" 甚至带 "#自定义头"。取第一段主机名即可 ——
		// 多主机随机是为了在大流量下不显眼，而这里的连接数以个位计。
		v := p.ObfsParam
		if i := strings.Index(v, "#"); i >= 0 {
			v = v[:i]
		}
		if i := strings.Index(v, ","); i >= 0 {
			v = v[:i]
		}
		if v != "" {
			host = v
		}
	}
	return &httpSimpleConn{Conn: c, host: host, port: p.Port}
}

// httpSimpleUserAgents 假请求里用的 UA。
//
// ★ 与 core/outbound 那套"指纹自带 UA"是**两件事**：那边的 UA 是给目标站点看的，
// 这里的 UA 是给代理链路上的中间设备看的，两者不共用一张表也不该共用 —— 这条
// 连接上真正的 HTTP 请求是加密的，中间设备只看得到这一个假头。
var httpSimpleUserAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:134.0) Gecko/20100101 Firefox/134.0",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
}

func (c *httpSimpleConn) Write(b []byte) (int, error) {
	if c.sent {
		return c.Conn.Write(b)
	}
	c.sent = true

	head, rest := httpSimpleSplit(b)

	var sb strings.Builder
	sb.WriteString("GET /")
	sb.WriteString(httpSimplePercentEncode(head))
	sb.WriteString(" HTTP/1.1\r\nHost: ")
	sb.WriteString(c.host)
	if c.port != "80" {
		sb.WriteString(":")
		sb.WriteString(c.port)
	}
	sb.WriteString("\r\nUser-Agent: ")
	sb.WriteString(httpSimpleUserAgents[mathrand.Intn(len(httpSimpleUserAgents))])
	sb.WriteString("\r\nAccept: text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8" +
		"\r\nAccept-Language: en-US,en;q=0.8" +
		"\r\nAccept-Encoding: gzip, deflate" +
		"\r\nDNT: 1" +
		"\r\nConnection: keep-alive\r\n\r\n")

	out := append([]byte(sb.String()), rest...)
	if _, err := c.Conn.Write(out); err != nil {
		return 0, err
	}
	return len(b), nil
}

// httpSimpleSplit 决定首包里有多少字节编进 URI、多少跟在头后面当请求体。
//
// 照搬 shadowsocksr 的 client_encode：头部太长时只取一段进 URI（URI 无限长会
// 显眼），其余当 body。首包通常只有几十字节，走的是"整包进 URI"那条。
func httpSimpleSplit(b []byte) (head, rest []byte) {
	headSize := httpSimpleHeadSize(b, 30)
	headLen := len(b)
	if len(b)-headSize > 64 {
		headLen = headSize + mathrand.Intn(65)
	}
	if headLen > len(b) {
		headLen = len(b)
	}
	return b[:headLen], b[headLen:]
}

func httpSimpleHeadSize(b []byte, def int) int {
	if len(b) < 2 {
		return def
	}
	switch b[0] & 0x0F {
	case 1:
		return 7
	case 4:
		return 19
	case 3:
		return 4 + int(b[1])
	}
	return def
}

func httpSimplePercentEncode(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b) * 3)
	const hex = "0123456789abcdef"
	for _, x := range b {
		sb.WriteByte('%')
		sb.WriteByte(hex[x>>4])
		sb.WriteByte(hex[x&0x0F])
	}
	return sb.String()
}

func (c *httpSimpleConn) Read(b []byte) (int, error) {
	if len(c.pending) > 0 {
		n := copy(b, c.pending)
		c.pending = c.pending[n:]
		return n, nil
	}
	if c.recvDone {
		return c.Conn.Read(b)
	}

	tmp := make([]byte, 4096)
	for {
		n, err := c.Conn.Read(tmp)
		if n > 0 {
			c.hdrBuf = append(c.hdrBuf, tmp[:n]...)
			if i := bytes.Index(c.hdrBuf, []byte("\r\n\r\n")); i >= 0 {
				c.recvDone = true
				body := c.hdrBuf[i+4:]
				c.hdrBuf = nil
				m := copy(b, body)
				if m < len(body) {
					c.pending = append([]byte(nil), body[m:]...)
				}
				if m > 0 {
					return m, nil
				}
				// 假响应头之后这一批没有数据，继续读真数据。
				return c.Conn.Read(b)
			}
			if len(c.hdrBuf) > httpSimpleMaxHeader {
				return 0, fmt.Errorf("ssr http_simple: 对方在 %d 字节内没有结束响应头，不像一个 ssr 服务端",
					httpSimpleMaxHeader)
			}
		}
		if err != nil {
			return 0, err
		}
	}
}

// ---------------------------------------------------------------- 对外的解析

// ProxyInfo 是一个代理 URL 解析出来的字段。
//
// ★★ **它带着凭据**（Password/Username）。存在的理由是让上层（配置界面、订阅
// 导入）能把一个粘进来的链接拆成表单里的字段，而不必自己再抄一份解析器 ——
// 两份解析器一定会漂移，而漂移的表现是"手工加的节点能用、订阅导进来的同一个
// 节点不能用"。拿到之后按机密处理：别进日志、别进错误信息（印之前过
// outbound.RedactProxy）。
type ProxyInfo struct {
	Scheme     string // http / https / socks5 / ss / ssr
	Host       string
	Port       string
	Username   string // http / socks5
	Password   string
	Method     string // ss / ssr 的加密方式
	Protocol   string // ssr
	ProtoParam string
	Obfs       string // ssr
	ObfsParam  string
	Tag        string // 节点名（# 后面那段）
}

// ParseProxyURL 解析一个代理 URL。**只解析，不拨号** —— 它不判断对方在不在。
//
// 认得的协议与 NewProxyDialer 完全一致（同一段代码）：一个 ParseProxyURL 能过而
// NewProxyDialer 建不起来的 URL 不该存在，否则界面上会出现"保存成功、然后永远
// 连不上"的节点。
func ParseProxyURL(raw string) (ProxyInfo, error) {
	scheme, rest, ok := strings.Cut(strings.TrimSpace(raw), "://")
	if !ok {
		return ProxyInfo{}, errors.New("不是一个代理 URL（缺少 scheme://）")
	}
	scheme = strings.ToLower(scheme)

	switch scheme {
	case "ss", "ssr":
		var (
			p   *ssParams
			err error
		)
		if scheme == "ss" {
			p, err = parseSsURL(rest)
		} else {
			p, err = parseSsrURL(rest)
		}
		if err != nil {
			return ProxyInfo{}, err
		}
		return ProxyInfo{
			Scheme: scheme, Host: p.Host, Port: p.Port,
			Password: p.Password, Method: p.Method,
			Protocol: p.Protocol, ProtoParam: p.ProtocolParam,
			Obfs: p.Obfs, ObfsParam: p.ObfsParam, Tag: p.Tag,
		}, nil

	case "http", "https", "socks5", "socks5h":
		u, err := url.Parse(raw)
		if err != nil {
			return ProxyInfo{}, err
		}
		host, port := u.Hostname(), u.Port()
		if port == "" {
			switch scheme {
			case "http":
				port = "80"
			case "https":
				port = "443"
			default:
				port = "1080"
			}
		}
		info := ProxyInfo{Scheme: scheme, Host: host, Port: port, Tag: u.Fragment}
		if u.User != nil {
			info.Username = u.User.Username()
			info.Password, _ = u.User.Password()
		}
		return info, nil
	}
	return ProxyInfo{}, errors.New("scheme " + scheme + " is not supported; supported: http, https, socks5, ss, ssr")
}
