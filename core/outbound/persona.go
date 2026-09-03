package outbound

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/volts-dev/volts/transport"
	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

// tlsClientConfig 是握手用的 TLS 策略。**生产路径上没有任何地方改它** ——
// 它是变量只为让测试能对着 httptest 的自签名证书跑。
//
// 零值即"校验证书"。指纹伪装是给对方看的，不是拿来关掉自己这边的校验的。
var tlsClientConfig = &tls.Config{}

// handshakeTimeout 是 TLS 握手的独立期限。
//
// ★ http.Transport 的 TLSHandshakeTimeout 在这里**不生效** —— 那个字段只管
// Transport 自己做的握手，用了 DialTLSContext 就绕过了它。不自己设的话，握手只
// 受调用方 ctx 管，一个吞包的代理能把整个请求预算耗在握手上。
const handshakeTimeout = 10 * time.Second

type (
	// proxyEndpoint 是一个代理出口。nil 的 *proxyEndpoint 表示直连。
	proxyEndpoint struct {
		url    string // 原样保留，只用于日志（印之前过 RedactProxy）
		dialer proxy.Dialer
	}

	// persona 是一个出站身份：一套指纹 + 一个 UA + 一个出口 + **自己的连接池**。
	persona struct {
		label string
		fp    *transport.Fingerprint // nil = 不伪装
		ua    string                 // 该指纹的第 n 条 UA，见 buildPersonas
		out   *proxyEndpoint         // nil = 直连

		h1 *http.Transport
		h2 *http2.Transport

		// sem 是这条线路上的**并发闸门**，容量 = 允许同时在飞的请求数（默认 1）。
		// 拿不到位置的请求去等，而不是从同一个身份上再拨一条连接出去 —— 理由见
		// 包注释「同一条线路上不并行」。
		sem chan struct{}

		mu       sync.Mutex
		protos   map[string]string // addr → ALPN 协商结果
		failures int
		until    time.Time // 冷却到期时刻
	}
)

// newPersona 建一个身份。index 决定用该指纹的第几条 UA（不同平台）。
// slots 是这条线路上允许同时在飞的请求数，<1 一律当 1。
func newPersona(fp *transport.Fingerprint, index int, out *proxyEndpoint, slots int) *persona {
	if slots < 1 {
		slots = 1
	}
	p := &persona{
		fp:     fp,
		ua:     fp.UserAgent(index), // fp 为 nil 时返回空串，见 Fingerprint.UserAgent
		out:    out,
		protos: map[string]string{},
		sem:    make(chan struct{}, slots),
	}
	p.label = personaLabel(fp, p.ua, out)

	p.h1 = &http.Transport{
		DialContext:           p.dialRaw,
		ResponseHeaderTimeout: 20 * time.Second,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   handshakeTimeout, // 只在不伪装那条路上生效
	}

	// 没有指纹（没导入 core/ja3，或显式关掉）：让 http.Transport 用它自己的
	// crypto/tls（含 HTTP/2 自动升级）。这条路与本包出现之前的行为逐字一致，
	// 只是多了一个可选的代理出口。
	if fp == nil || transport.Ja3Dialer() == nil {
		p.fp = nil
		p.h1.ForceAttemptHTTP2 = true
		return p
	}

	p.h1.DialTLSContext = p.dialTLS
	p.h2 = &http2.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return p.dialTLS(ctx, network, addr)
		},
		// 半死连接（代理断了但 FIN 没回来）会让复用的连接一直挂着。h2 只有一条
		// 连接，挂住就是整个身份哑掉，所以设个 ping。h1 靠 IdleConnTimeout。
		ReadIdleTimeout: 30 * time.Second,
		PingTimeout:     10 * time.Second,

		// ★★ **故意不调 SETTINGS**（MaxHeaderListSize / MaxReadFrameSize /
		// MaxDecoderHeaderTableSize / 窗口大小）。实测见包注释：调完既不像 Chrome
		// （setting 5 Chrome 不发而 Go 必发、SETTINGS 顺序不同、伪头顺序硬编码），
		// 又把一个**全世界最常见**的 Go 指纹变成了一个独一无二的自制指纹 ——
		// 从"混在人群里"变成"世界上只有你在发"。半吊子的伪装比不伪装更显眼。
	}
	return p
}

func personaLabel(fp *transport.Fingerprint, ua string, out *proxyEndpoint) string {
	label := "no-fingerprint"
	if fp != nil {
		label = fp.Name + "/" + shortPlatform(ua)
	}
	if out == nil {
		return label + " (直连)"
	}
	return label + " via " + RedactProxy(out.url)
}

// shortPlatform 从 UA 里认出平台，只为让日志里的身份名一眼能分辨
// （"chrome_133/win" 与 "chrome_133/mac" 是池子里两个不同的身份）。
func shortPlatform(ua string) string {
	switch platformHint(ua) {
	case `"Windows"`:
		return "win"
	case `"macOS"`:
		return "mac"
	case `"Linux"`:
		return "linux"
	case `"Android"`:
		return "android"
	case `"iOS"`:
		return "ios"
	}
	return "?"
}

// roundTrip 用本身份发一次请求。
func (p *persona) roundTrip(req *http.Request) (*http.Response, error) {
	req = decorate(req, p.fp, p.ua)

	// http（非 TLS）没有指纹可言，也没有 ALPN 要探 —— 直接走 h1，代理仍然生效。
	if p.fp == nil || req.URL.Scheme != "https" {
		return p.h1.RoundTrip(req)
	}

	addr := canonicalAddr(req.URL)
	proto, known := p.proto(addr)
	if !known {
		var err error
		if proto, err = p.probeProto(req.Context(), addr); err != nil {
			return nil, err
		}
	}
	if proto == http2.NextProtoTLS {
		return p.h2.RoundTrip(req)
	}
	return p.h1.RoundTrip(req)
}

// probeProto 拨一条连接只为读 ALPN 结果，读完就关。
//
// 为什么要单独探一次：Go 的 http.Transport 读 ALPN 用的是**类型断言 *tls.Conn**，
// 而 utls 的连接不是那个类型 —— 于是无论对方选了什么，Transport 都按 HTTP/1.1
// 说话。对方按 ALPN 选了 h2 而我们发 HTTP/1.1 的报文，得到的是一个与"站点改了
// 返回格式"分不开的解析错误。
//
// 代价是每个新的 host:port 多一次握手，**一次**（结果缓存在 p.protos 里）。换来的
// 是不必写"把探测连接缓存起来给下一次请求用"那套 —— 那要在 RoundTrip 之间跨
// goroutine 传连接，是竞态的常客。
func (p *persona) probeProto(ctx context.Context, addr string) (string, error) {
	conn, err := p.dialTLS(ctx, "tcp", addr)
	if err != nil {
		return "", err
	}
	conn.Close()

	proto, _ := p.proto(addr) // dialTLS 已经记下了
	return proto, nil
}

// dialTLS 拨一条连接并用本身份的指纹完成握手。
func (p *persona) dialTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	dial := transport.Ja3Dialer()
	if dial == nil {
		// 建池时就查过了；到这里还是 nil 只可能是运行期注册被撤了。
		// **报错而不是退回普通 TLS** —— 静默退回会让伪装悄悄失效。
		return nil, transport.ErrJa3NotRegistered
	}

	rawConn, err := p.dialRaw(ctx, network, addr)
	if err != nil {
		return nil, err
	}

	// 握手的独立期限，见 handshakeTimeout。
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(handshakeTimeout)
	} else if d := time.Now().Add(handshakeTimeout); d.Before(deadline) {
		deadline = d
	}
	if err := rawConn.SetDeadline(deadline); err != nil {
		rawConn.Close()
		return nil, err
	}

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}

	// dial 在失败时自己关 rawConn（core/ja3 的契约）。
	conn, err := dial(rawConn, host, tlsClientConfig, p.fp.Name)
	if err != nil {
		return nil, err
	}

	// ★★ 握手完了必须**把 deadline 清掉**。留着的话这条连接会在那个时刻突然
	// 读写失败 —— 而它早已被连接池复用给了另一个请求，症状是几分钟后随机冒出
	// 一个 i/o timeout，与握手没有任何字面上的关系。
	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, err
	}

	if np, ok := conn.(transport.INegotiatedProtocol); ok {
		p.setProto(addr, np.NegotiatedProtocol())
	}
	return conn, nil
}

// dialRaw 建底层 TCP 连接：走本身份的代理，没有代理就直连。
func (p *persona) dialRaw(ctx context.Context, network, addr string) (net.Conn, error) {
	if p.out == nil {
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
	}
	// transport.NewProxyDialer 回的三种（HTTP CONNECT / HTTPS CONNECT / SOCKS5）
	// 都实现了 ContextDialer。退路留着是因为 proxy.Dialer 接口本身不要求它 ——
	// 走到退路上就意味着这次拨号不受 ctx 管，只有 TCP 自己的超时兜底。
	if cd, ok := p.out.dialer.(proxy.ContextDialer); ok {
		return cd.DialContext(ctx, network, addr)
	}
	return p.out.dialer.Dial(network, addr)
}

func (p *persona) proto(addr string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.protos[addr]
	return v, ok
}

func (p *persona) setProto(addr, proto string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.protos[addr] = proto
}

// tryAcquire 占住这条线路上的一个位置；占不到说明已经有请求在飞，回 false。
//
// 用带缓冲的 channel 而不是计数器 + 条件变量：占位与判满在这里是**同一个动作**，
// 分成两步就有 TOCTOU —— 两个 goroutine 同时看到"空闲"然后一起发出去，而这正是
// 要禁掉的那件事。
func (p *persona) tryAcquire() bool {
	select {
	case p.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

// busy 这条线路上是不是已经满了。只用于挑身份时的偏好，不作为占位的判据
// （那是 tryAcquire 的事）。
func (p *persona) busy() bool { return len(p.sem) >= cap(p.sem) }

func (p *persona) available(now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !now.Before(p.until)
}

func (p *persona) cooldownEnd() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.until
}

// markSuccess —— 一次成功就完全恢复。
//
// 与 volts selector 的熔断器同一个取舍：代理挂掉多半是暂时的，让一个刚证明自己
// 能用的出口继续背着历史失败计数，只会让它下次失败时被踢得更久。
func (p *persona) markSuccess() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failures = 0
	p.until = time.Time{}
}

// markFailure 让本身份冷却一段时间，连续失败则指数退避（上限 cooldownMax）。
func (p *persona) markFailure(base time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failures++
	p.until = time.Now().Add(backoff(base, p.failures))
}

// markBlocked —— 对方明确把我们挡回来了（403/429…），不是网络故障。
//
// 与 markFailure 只差一点：对方若在 Retry-After 里说了要等多久，就听它的 ——
// 那是**它自己**给出的解封时间，比任何退避曲线都准。仍然受 cooldownMax 封顶，
// 理由见 retryAfter 的注释。
//
// 失败计数照样加：Retry-After 到期后若还是被挡，退避得能自己长上去。
func (p *persona) markBlocked(base, retryAfter time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failures++

	d := retryAfter
	if d <= 0 {
		d = backoff(base, p.failures)
	} else if d > cooldownMax {
		d = cooldownMax
	}
	p.until = time.Now().Add(d)
}

// backoff 第 n 次连续失败该冷却多久：base 起步，每次翻倍，封顶 cooldownMax。
func backoff(base time.Duration, n int) time.Duration {
	if base <= 0 {
		base = 30 * time.Second
	}
	d := base
	for i := 1; i < n && d < cooldownMax; i++ {
		d *= 2
	}
	if d > cooldownMax {
		d = cooldownMax
	}
	return d
}

// closeIdle 扔掉本身份池子里的空闲连接。
//
// 被挡回来之后必须做这件事：风控的判定常常绑在**连接/会话**上，而不是每个请求
// 重新算一次。留着那条已经被打上标记的连接，冷却结束后的第一个请求还是从它出去，
// 冷却就白等了。
//
// ALPN 的探测结果不必扔 —— 用什么协议是站点的属性，与是哪条连接无关。
func (p *persona) closeIdle() {
	p.h1.CloseIdleConnections()
	if p.h2 != nil {
		p.h2.CloseIdleConnections()
	}
}

// status 给 Status() 用的一行描述：身份名，外加它现在为什么不接活。
//
// 两种"不接活"要分开印：**冷却中**是被挡或失败之后的惩罚（要等），**使用中**是
// 这条线路上已经有一个请求在飞（不必等太久）。看到一排"使用中"说明并发被闸门
// 挡住了 —— 那是设计如此，不是故障；看到一排"冷却中"才是真出事了。
// 不印的话诊断端点会显示一个看着健康、实际一个请求都不承担的身份。
func (p *persona) status(now time.Time) string {
	end := p.cooldownEnd()
	if now.Before(end) {
		return fmt.Sprintf("%s —— 冷却中，还剩 %s", p.label, end.Sub(now).Round(time.Second))
	}
	if p.busy() {
		return p.label + " —— 使用中"
	}
	return p.label
}

// canonicalAddr 把 URL 变成 host:port，补上 scheme 的默认端口。
func canonicalAddr(u *url.URL) string {
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		if u.Scheme == "http" {
			port = "80"
		} else {
			port = "443"
		}
	}
	return net.JoinHostPort(host, port)
}
