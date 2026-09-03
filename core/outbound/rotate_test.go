package outbound

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/volts-dev/volts/transport"
)

// 被挡回来（403/429）与网络错误是两件不同的事：站在 RoundTripper 的角度前者是一次
// **成功**的往返，不特殊处理的话被风控挡住的身份会带着同一套指纹一遍遍撞上去。
//
// 这一组用明文 httptest（http://）跑：轮换与冷却的逻辑跟 TLS 无关，而真指纹的握手
// 要空导入 core/ja3 —— 本包的一条设计约束就是不把 utls 拖进主模块的依赖树。

// counting 起一个按第 n 次请求决定状态码的服务，并返回收到的请求数。
func counting(t *testing.T, status func(n int32) int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var seen atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code := status(seen.Add(1))
		w.WriteHeader(code)
		if code == http.StatusOK {
			io.WriteString(w, "ok")
		} else {
			io.WriteString(w, "blocked")
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestBlockedStatusRotatesToAnotherIdentity(t *testing.T) {
	srv, seen := counting(t, func(n int32) int {
		if n <= 2 {
			return http.StatusForbidden
		}
		return http.StatusOK
	})

	r := testRotatorN(3)
	rsp, err := (&http.Client{Transport: r}).Get(srv.URL)
	if err != nil {
		t.Fatalf("被挡两次之后应当由第三个身份成功，实际报错: %v", err)
	}
	defer rsp.Body.Close()

	if rsp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP %d，想要 200", rsp.StatusCode)
	}
	if got := seen.Load(); got != 3 {
		t.Fatalf("服务器收到 %d 次请求，想要 3（前两次被挡，各换一个身份）", got)
	}

	now := time.Now()
	if r.personas[0].available(now) || r.personas[1].available(now) {
		t.Error("被挡回的身份没有进冷却 —— 下一次请求还会从它出去")
	}
	if !r.personas[2].available(now) {
		t.Error("成功的那个身份不该在冷却里")
	}
}

// TestAllBlockedReturnsTheResponseNotAnError —— 全部身份都被挡时必须把**最后那个
// 响应**原样交还。
//
// 调用方要看见 403 本身：connect_components 的 store 正是靠状态码分辨"被对方拒了"
// 与"网络故障"的，编一句错误会把这条信息弄丢。body 也必须还能读 —— 换身份时关掉的
// 是上一个响应，不是要交出去的这个。
func TestAllBlockedReturnsTheResponseNotAnError(t *testing.T) {
	srv, seen := counting(t, func(int32) int { return http.StatusForbidden })

	r := testRotatorN(3)
	rsp, err := (&http.Client{Transport: r}).Get(srv.URL)
	if err != nil {
		t.Fatalf("全部被挡时不该返回 error（403 是一次有效的答复）: %v", err)
	}
	defer rsp.Body.Close()

	if rsp.StatusCode != http.StatusForbidden {
		t.Fatalf("HTTP %d，想要 403", rsp.StatusCode)
	}
	body, err := io.ReadAll(rsp.Body)
	if err != nil || string(body) != "blocked" {
		t.Fatalf("交还的响应 body 读不出来（%q, %v）—— 大概是被提前关掉了", body, err)
	}
	if got := seen.Load(); got != 3 {
		t.Fatalf("服务器收到 %d 次请求，想要 3（maxAttempts）", got)
	}
}

// TestNonBlockingStatusIsReturnedImmediately —— 404/500 不算被挡。
//
// 换身份既救不了一个 500，也只是把发给一个本就吃力的站点的流量乘以 maxAttempts。
func TestNonBlockingStatusIsReturnedImmediately(t *testing.T) {
	srv, seen := counting(t, func(int32) int { return http.StatusInternalServerError })

	r := testRotatorN(3)
	rsp, err := (&http.Client{Transport: r}).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer rsp.Body.Close()

	if got := seen.Load(); got != 1 {
		t.Fatalf("500 也去换身份重试了：服务器收到 %d 次请求", got)
	}
	if !r.personas[0].available(time.Now()) {
		t.Error("500 不该让身份进冷却")
	}
}

// TestRotateOnStatusIsConfigurable —— 置空就关掉这个行为。
func TestRotateOnStatusIsConfigurable(t *testing.T) {
	restore := cfg.RotateOnStatus
	cfg.RotateOnStatus = nil
	defer func() { cfg.RotateOnStatus = restore }()

	srv, seen := counting(t, func(int32) int { return http.StatusForbidden })
	rsp, err := (&http.Client{Transport: testRotatorN(3)}).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	rsp.Body.Close()
	if got := seen.Load(); got != 1 {
		t.Fatalf("关掉之后仍然换身份重试了 %d 次", got)
	}
}

// ── Retry-After ───────────────────────────────────────────────────────────

func TestRetryAfterSeconds(t *testing.T) {
	rsp := &http.Response{Header: http.Header{"Retry-After": []string{"120"}}}
	if got := retryAfter(rsp, time.Now()); got != 2*time.Minute {
		t.Fatalf("retryAfter = %v，想要 2m", got)
	}
}

func TestRetryAfterHttpDate(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	rsp := &http.Response{Header: http.Header{
		"Retry-After": []string{now.Add(90 * time.Second).Format(http.TimeFormat)},
	}}
	got := retryAfter(rsp, now)
	if got < 89*time.Second || got > 91*time.Second {
		t.Fatalf("retryAfter = %v，想要 ~90s", got)
	}
}

// TestRetryAfterIgnoresGarbage —— 认不出来就回 0（用我们自己的退避），不是 panic
// 也不是 0 秒冷却：Retry-After 是对方写的，什么都可能出现在里面。
func TestRetryAfterIgnoresGarbage(t *testing.T) {
	past := time.Now().UTC().Add(-time.Hour).Format(http.TimeFormat)
	for _, v := range []string{"", "  ", "soon", "-5", "0", past} {
		rsp := &http.Response{Header: http.Header{"Retry-After": []string{v}}}
		if got := retryAfter(rsp, time.Now()); got != 0 {
			t.Errorf("Retry-After=%q 解出了 %v，想要 0", v, got)
		}
	}
}

func TestMarkBlockedHonorsRetryAfter(t *testing.T) {
	p := newPersona(nil, 0, nil, 1)
	before := time.Now()
	p.markBlocked(30*time.Second, 2*time.Minute) // 退避第一次只有 30s
	if d := p.cooldownEnd().Sub(before); d < 119*time.Second || d > 121*time.Second {
		t.Fatalf("冷却 %v，想要 ~2m（听对方的 Retry-After）", d)
	}
}

// TestMarkBlockedCapsRetryAfter —— 对方说等一天也不能真停一天：一个出故障的或被
// 劫持的响应不该能把一个身份永久踢出去。它真要我们等更久，下一次拒绝会让退避自己
// 长上来。
func TestMarkBlockedCapsRetryAfter(t *testing.T) {
	p := newPersona(nil, 0, nil, 1)
	before := time.Now()
	p.markBlocked(30*time.Second, 24*time.Hour)
	if d := p.cooldownEnd().Sub(before); d > cooldownMax+time.Second {
		t.Fatalf("冷却 %v 超过上限 %v", d, cooldownMax)
	}
}

// ── 常备与备用 ────────────────────────────────────────────────────────────

// TestReservesOnlyUsedWhenActivesAreBlocked —— 备用身份平时一次都不该被取到。
//
// "一个出口一个身份"是稳态的规矩（一个真人不会每五分钟换一次浏览器）；备用身份
// 是被挡之后才成立的例外。两者混起来的话，稳态就退回成"同一个 IP 上轮换指纹"，
// 而那正是这个包论证过的、比不轮换更可疑的做法。
func TestReservesOnlyUsedWhenActivesAreBlocked(t *testing.T) {
	r := testRotatorN(1)
	r.reserves = []*persona{labelled("R0"), labelled("R1")}

	for i := 0; i < 5; i++ {
		if got := pick(t, r); got != r.personas[0] {
			t.Fatalf("第 %d 次就动用了备用身份 %s", i+1, got.label)
		}
	}

	r.personas[0].markFailure(time.Minute)
	if got := pick(t, r); got != r.reserves[0] {
		t.Fatalf("常备冷却后取到 %s，想要 R0", got.label)
	}
	if got := pick(t, r); got != r.reserves[1] {
		t.Fatalf("备用身份之间没有轮转，取到 %s", got.label)
	}

	// 常备一恢复就该回到它 —— 备用只是顶班。
	r.personas[0].markSuccess()
	if got := pick(t, r); got != r.personas[0] {
		t.Fatalf("常备恢复后仍在用备用身份 %s", got.label)
	}
}

// TestFailsOpenAcrossReserves —— 常备与备用全在冷却时也必须挑一个出去。
// 一个把 100% 出口都摘掉的熔断器，比它要防的故障更糟。
func TestFailsOpenAcrossReserves(t *testing.T) {
	r := testRotatorN(1)
	r.reserves = []*persona{labelled("R0")}
	r.personas[0].markFailure(time.Hour)
	r.reserves[0].markFailure(time.Minute)

	got, done := r.take(time.Now())
	if got == nil {
		t.Fatal("全部冷却时返回了 nil")
	}
	done()
	if got != r.reserves[0] {
		t.Errorf("没挑冷却最早到期的那个，取到 %s", got.label)
	}
}

// TestReservesAreTheLeftoverFingerprints —— 多出来的指纹不丢，留作备用。
func TestReservesAreTheLeftoverFingerprints(t *testing.T) {
	fps := []*transport.Fingerprint{{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"}}

	active, reserve := assemble(fps, []*proxyEndpoint{nil}, false, 1)
	if len(active) != 1 || len(reserve) != 3 {
		t.Fatalf("单出口：常备 %d 备用 %d，想要 1 和 3", len(active), len(reserve))
	}

	// 显式点名 profiles = "我就要这几个"：全部常备，不该有它没点名的身份在背后顶上。
	active, reserve = assemble(fps, []*proxyEndpoint{nil}, true, 1)
	if len(active) != 4 || len(reserve) != 0 {
		t.Fatalf("显式点名：常备 %d 备用 %d，想要 4 和 0", len(active), len(reserve))
	}
}

func TestAllDirectDetectsAnyProxy(t *testing.T) {
	if !allDirect([]*persona{labelled("A"), labelled("B")}) {
		t.Error("全直连却报成有代理")
	}
	viaProxy := labelled("C")
	viaProxy.out = &proxyEndpoint{url: "socks5://127.0.0.1:1080"}
	if allDirect([]*persona{labelled("A"), viaProxy}) {
		t.Error("有一个代理出口就不该报 allDirect —— 那条 WARN 会误导人去配代理")
	}
}

// ── 重发与 body ───────────────────────────────────────────────────────────

// TestBlockedPostReplaysFullBody —— 换身份重发时 body 必须重新拿一份。
//
// http.NewRequest 对 strings/bytes 的 body 会自动填好 GetBody。不用它的话第二次
// 发出去的是个**空**请求，而对方只会回一句"参数不对" —— 与指纹、代理都没关系的
// 一个错误，排查会从头错到尾。
func TestBlockedPostReplaysFullBody(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, string(b))
		if len(got) == 1 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	rsp, err := (&http.Client{Transport: testRotatorN(2)}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	rsp.Body.Close()

	if len(got) != 2 {
		t.Fatalf("服务器收到 %d 次请求，想要 2", len(got))
	}
	for i, b := range got {
		if b != "payload" {
			t.Errorf("第 %d 次收到的 body = %q，想要 payload", i+1, b)
		}
	}
}

// TestBlockedPostWithoutGetBodyIsNotReplayed —— 拿不回 body 就不重发。
//
// 静默送出一个空请求，比把 403 原样交还给调用方更糟：前者的症状是"对方说参数不对"。
func TestBlockedPostWithoutGetBodyIsNotReplayed(t *testing.T) {
	srv, seen := counting(t, func(int32) int { return http.StatusForbidden })

	// 不是 strings/bytes 的 Reader → http.NewRequest 填不出 GetBody。
	body := struct{ io.Reader }{strings.NewReader("payload")}
	req, err := http.NewRequest(http.MethodPost, srv.URL, body)
	if err != nil {
		t.Fatal(err)
	}
	if req.GetBody != nil {
		t.Fatal("这个用例要的是 GetBody 为 nil 的请求")
	}

	rsp, err := (&http.Client{Transport: testRotatorN(3)}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	rsp.Body.Close()
	if got := seen.Load(); got != 1 {
		t.Fatalf("没有 GetBody 却重发了 %d 次 —— 第二次发的是个空请求", got)
	}
}

// ── 诊断 ──────────────────────────────────────────────────────────────────

// TestStatusShowsCooldown —— 冷却是"为什么现在出去的不是它"的唯一解释。
func TestStatusShowsCooldown(t *testing.T) {
	p := labelled("A")
	if got := p.status(time.Now()); got != "A" {
		t.Fatalf("没冷却时 status = %q，想要 A", got)
	}
	p.markFailure(time.Minute)
	if got := p.status(time.Now()); !strings.Contains(got, "冷却中") {
		t.Fatalf("冷却中的身份 status = %q，看不出它不承担请求", got)
	}
}

func labelled(name string) *persona {
	p := newPersona(nil, 0, nil, 1)
	p.label = name
	return p
}

// ── 换的到底是不是"身份" ───────────────────────────────────────────────────

const (
	uaChrome133 = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36"
	uaFirefox   = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:120.0) Gecko/20100101 Firefox/120.0"
)

// withFakeJa3 让 newPersona 保留指纹。
//
// 它在**没有**已注册的握手实现时会把 fp 抹成 nil（宁可明说不伪装，也不假装伪装了），
// 而本包不能空导入 core/ja3：那是个反向依赖主模块的独立子模块，导进来就成了模块环。
// 注册的这个 dialer 永远不会被调用 —— 下面的用例走的是明文 http://，不握手。
func withFakeJa3(t *testing.T) {
	t.Helper()
	transport.RegisterJa3Dialer(func(net.Conn, string, *tls.Config, string) (net.Conn, error) {
		t.Error("不该握手：本用例走的是明文 http://")
		return nil, io.EOF
	})
	t.Cleanup(func() { transport.RegisterJa3Dialer(nil) })
}

// TestRotationChangesTheIdentityOnTheWire —— 被挡之后**对方看到的**确实换了个人。
//
// 上面几条验的是"换了一个 persona 对象"，这条验的是那件事在网线上成不成立：
// 第二次请求的 UA 必须是另一个身份的，且 client hints 跟着 UA 走（Firefox 不发）。
// 少了这条，"每个身份自己的连接池 + decorate 克隆请求"哪天被改坏都不会有人发现 ——
// 症状是轮换照常发生、出去的却始终是同一个 UA。
func TestRotationChangesTheIdentityOnTheWire(t *testing.T) {
	withFakeJa3(t)

	type seen struct {
		ua   string
		hint string
	}
	var got []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, seen{r.UserAgent(), r.Header.Get("sec-ch-ua")})
		if len(got) == 1 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	fps := []*transport.Fingerprint{
		{Name: "chrome", Family: transport.FamilyChrome, UserAgents: []string{uaChrome133}},
		{Name: "firefox", Family: transport.FamilyFirefox, UserAgents: []string{uaFirefox}},
	}
	active, _ := assemble(fps, []*proxyEndpoint{nil, nil}, false, 1)

	rsp, err := (&http.Client{Transport: testRotator(active...)}).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	rsp.Body.Close()

	if len(got) != 2 {
		t.Fatalf("服务器收到 %d 次请求，想要 2", len(got))
	}
	if got[0].ua != uaChrome133 {
		t.Errorf("第一次的 UA = %q", got[0].ua)
	}
	if got[1].ua != uaFirefox {
		t.Errorf("换身份之后 UA = %q，想要 Firefox 的 —— 对方看到的还是同一个人", got[1].ua)
	}
	if got[0].hint == "" {
		t.Error("Chrome 身份没发 sec-ch-ua")
	}
	if got[1].hint != "" {
		t.Errorf("Firefox 身份发了 sec-ch-ua: %q —— 那是 Chromium 才有的头，比两样都不发更容易被认出来", got[1].hint)
	}
}

// TestEachIdentityHasItsOwnConnectionPool —— 两个身份不能共用一条连接。
//
// 共用的话，一条以 A 的指纹握过手的连接会去发一个 UA 写着 B 的请求 —— 同一条
// TLS 连接上出现两个身份，比不伪装更容易被认出来。这里用明文连接的**远端端口**
// 当替身：同一条连接复用时端口相同，不同连接必然不同。
func TestEachIdentityHasItsOwnConnectionPool(t *testing.T) {
	withFakeJa3(t)

	var ports []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, port, _ := net.SplitHostPort(r.RemoteAddr)
		ports = append(ports, port)
		if len(ports) == 1 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	fps := []*transport.Fingerprint{
		{Name: "chrome", Family: transport.FamilyChrome, UserAgents: []string{uaChrome133}},
		{Name: "firefox", Family: transport.FamilyFirefox, UserAgents: []string{uaFirefox}},
	}
	active, _ := assemble(fps, []*proxyEndpoint{nil, nil}, false, 1)

	rsp, err := (&http.Client{Transport: testRotator(active...)}).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	rsp.Body.Close()

	if len(ports) != 2 {
		t.Fatalf("服务器收到 %d 次请求，想要 2", len(ports))
	}
	if ports[0] == ports[1] {
		t.Fatal("两个身份用了同一条连接 —— 连接池被共享了")
	}
}

// ---------------------------------------------------------------- SetProxies

// TestSetProxies_ReplacesPoolAtRuntime —— 界面上改完代理要**立刻**生效。
func TestSetProxies_ReplacesPoolAtRuntime(t *testing.T) {
	defer restoreRuntimeProxies()

	r := &rotator{}
	r.once.Do(func() {})

	if err := r.setProxies([]string{"socks5://127.0.0.1:1080", "socks5://127.0.0.1:1081"}); err != nil {
		t.Fatalf("setProxies: %v", err)
	}
	r.mu.Lock()
	got := len(r.personas)
	direct := r.allDirect
	r.mu.Unlock()
	if got != 2 {
		t.Fatalf("常备身份数 = %d，想要 2（一个出口一个身份）", got)
	}
	if direct {
		t.Fatal("配了代理却认为是直连")
	}

	// 撤销覆盖 → 回到 config.json（本测试里是空的）→ 直连一个身份。
	if err := r.setProxies(nil); err != nil {
		t.Fatalf("setProxies(nil): %v", err)
	}
	r.mu.Lock()
	got, direct = len(r.personas), r.allDirect
	r.mu.Unlock()
	if !direct {
		t.Fatalf("撤销覆盖后应回到直连，得到 %d 个非直连身份", got)
	}
}

// TestSetProxies_KeepsOldPoolOnFailure —— 新列表全不可用时，**不能**把正在工作的
// 出口换掉。换上一个空池子等于把出站整个掐掉，而这只是一次保存失败。
func TestSetProxies_KeepsOldPoolOnFailure(t *testing.T) {
	defer restoreRuntimeProxies()

	old := cfg.AllowDirect
	cfg.AllowDirect = false
	defer func() { cfg.AllowDirect = old }()

	r := &rotator{}
	r.once.Do(func() {})
	if err := r.setProxies([]string{"socks5://127.0.0.1:1080"}); err != nil {
		t.Fatalf("setProxies: %v", err)
	}
	r.mu.Lock()
	before := r.personas
	r.mu.Unlock()

	// 一个协议都不认识的列表 → resolveProxies 一个都建不起来 → ErrNoUsableProxy。
	err := r.setProxies([]string{"vmess://nope", "hysteria2://nope"})
	if !errors.Is(err, ErrNoUsableProxy) {
		t.Fatalf("想要 ErrNoUsableProxy，得到 %v", err)
	}
	r.mu.Lock()
	after := r.personas
	r.mu.Unlock()
	if len(after) != len(before) || (len(after) > 0 && after[0] != before[0]) {
		t.Fatal("建池失败之后旧身份池被换掉了 —— 一次失败的保存把出站掐了")
	}
	// 覆盖值也要还原，否则下一次重建会用上那个坏列表。
	if p := runtimeProxies.Load(); p == nil || len(*p) != 1 {
		t.Fatalf("失败后没有还原运行时覆盖：%v", p)
	}
}

// TestSetProxies_EmptyMeansDirect —— 空列表是"界面上一个都没启用"，等价于直连；
// 它与"配了但全建不起来"是两件不同的事（后者要 ErrNoUsableProxy）。
func TestSetProxies_EmptyMeansDirect(t *testing.T) {
	defer restoreRuntimeProxies()

	old := cfg.AllowDirect
	cfg.AllowDirect = false
	defer func() { cfg.AllowDirect = old }()

	r := &rotator{}
	r.once.Do(func() {})
	if err := r.setProxies([]string{}); err != nil {
		t.Fatalf("空列表不该报错，得到 %v", err)
	}
	r.mu.Lock()
	direct := r.allDirect
	r.mu.Unlock()
	if !direct {
		t.Fatal("空列表应当是直连")
	}
}

// TestSetProxies_AcceptsSsUrl —— ss/ssr 的 URL 必须能一路走到身份池里。
// 这条钉的是 transport.NewProxyDialer 的分派：少了它，机场订阅里的节点会被
// "跳过一个坏代理"那条路静默丢掉，池子看着建好了，其实一个出口都没有。
func TestSetProxies_AcceptsSsUrl(t *testing.T) {
	defer restoreRuntimeProxies()

	r := &rotator{}
	r.once.Do(func() {})
	if err := r.setProxies([]string{
		"ss://aes-256-gcm:secret@1.2.3.4:8388#HK01",
		"ssr://5.6.7.8:9000?method=chacha20-ietf&password=x&protocol=origin&obfs=http_simple",
	}); err != nil {
		t.Fatalf("setProxies: %v", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.personas) != 2 {
		t.Fatalf("常备身份数 = %d，想要 2", len(r.personas))
	}
	for _, p := range r.personas {
		if p.out == nil {
			t.Fatal("ss/ssr 的节点没有变成代理出口（被当成坏代理跳过了）")
		}
		// 标签会进日志。口令不能出现在里面。
		if strings.Contains(p.label, "secret") || strings.Contains(p.label, "password=x") {
			t.Fatalf("身份标签里带着口令：%s", p.label)
		}
	}
}

func restoreRuntimeProxies() { runtimeProxies.Store(nil) }

// TestRedactProxy_SsHidesCredentialsAndKeepsName —— ss/ssr 的凭据**整条**不能印。
//
// ★ 它们的凭据不在 `@` 前面：标准写法是 `ss://<一大段 base64>#节点名`，那段
// base64 里就是 method:password@host:port。按 `@` 打码的老写法对它完全无效
// （找不到 @ 就原样返回），于是口令进了日志、进了工单、进了截图。
func TestRedactProxy_SsHidesCredentialsAndKeepsName(t *testing.T) {
	cases := []struct{ in, wantOut, mustNotContain string }{
		{"ss://YWVzLTI1Ni1nY206aHVudGVyMg@1.2.3.4:8388#HK01", "ss://***#HK01", "YWVzLTI1Ni1nY206aHVudGVyMg"},
		{"ss://aes-256-gcm:hunter2@1.2.3.4:8388", "ss://***", "hunter2"},
		{"ssr://1.2.3.4:80?method=x&password=s3cret#%F0%9F%87%AD%F0%9F%87%B0+HK", "ssr://***#🇭🇰 HK", "s3cret"},
		// 老路径不能被改坏
		{"socks5://user:pw@gw:1080", "socks5://***@gw:1080", "pw@"},
		{"http://gw:8080", "http://gw:8080", "\x00"},
	}
	for _, c := range cases {
		got := RedactProxy(c.in)
		if got != c.wantOut {
			t.Errorf("RedactProxy(%q) = %q，想要 %q", c.in, got, c.wantOut)
		}
		if strings.Contains(got, c.mustNotContain) {
			t.Errorf("RedactProxy(%q) = %q —— 里面还有凭据 %q", c.in, got, c.mustNotContain)
		}
	}
}
