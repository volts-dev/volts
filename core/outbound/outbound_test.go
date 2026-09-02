package outbound

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/volts-dev/volts/transport"
)

// 注：**真指纹的握手**不在这里测 —— 那要空导入 core/ja3，而本包的一条设计约束
// 就是不把 utls 拖进主模块的依赖树（见包注释）。指纹能不能用由 core/ja3 自己的
// TestProfilesHandshake 与 boundary_live_test 负责；两者的集成在实际用它的模块里
// 验证（connect_components）。这里测的是本包自己的逻辑。

// ── 请求头推导 ────────────────────────────────────────────────────────────

func TestClientHintsAreDerivedFromUA(t *testing.T) {
	const ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36"
	h := browserHeaders(transport.FamilyChrome, ua)

	// 版本号必须与 UA 一致 —— 手工维护两份就是在等它们对不上。
	if got := h["sec-ch-ua"]; !strings.Contains(got, `"Google Chrome";v="133"`) {
		t.Errorf("sec-ch-ua 的版本没跟上 UA: %s", got)
	}
	if got := h["sec-ch-ua-platform"]; got != `"Windows"` {
		t.Errorf("sec-ch-ua-platform = %s, 想要 \"Windows\"", got)
	}
	if got := h["sec-ch-ua-mobile"]; got != "?0" {
		t.Errorf("sec-ch-ua-mobile = %s", got)
	}
}

func TestClientHintsVersionFollowsUA(t *testing.T) {
	for _, tc := range []struct{ ua, want string }{
		{"... Chrome/120.0.0.0 Safari/537.36", `v="120"`},
		{"... Chrome/133.0.0.0 Safari/537.36", `v="133"`},
	} {
		got := browserHeaders(transport.FamilyChrome, tc.ua)["sec-ch-ua"]
		if !strings.Contains(got, tc.want) {
			t.Errorf("UA %q → sec-ch-ua %q，想要含 %s", tc.ua, got, tc.want)
		}
	}
}

func TestPlatformFollowsUA(t *testing.T) {
	for ua, want := range map[string]string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/133.0.0.0":       `"Windows"`,
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Chrome/133.0.0.0": `"macOS"`,
		"Mozilla/5.0 (X11; Linux x86_64) Chrome/133.0.0.0":                 `"Linux"`,
	} {
		if got := platformHint(ua); got != want {
			t.Errorf("UA %q → %s, 想要 %s", ua, got, want)
		}
	}
}

// TestFirefoxSendsNoClientHints —— client hints 是 Chromium 的东西。
// 给一个 Firefox 的 TLS 指纹配上 sec-ch-ua，是一句立刻穿帮的自我介绍。
func TestFirefoxSendsNoClientHints(t *testing.T) {
	h := browserHeaders(transport.FamilyFirefox, "Mozilla/5.0 (Windows NT 10.0; rv:120.0) Gecko/20100101 Firefox/120.0")
	for k := range h {
		if strings.HasPrefix(k, "sec-ch-ua") {
			t.Errorf("Firefox 带上了 %s", k)
		}
	}
	// 但 Sec-Fetch-* 要有 —— 那是 Fetch Metadata 标准，Firefox 从 90 起也发。
	if h["sec-fetch-mode"] == "" {
		t.Error("Firefox 该发 sec-fetch-mode")
	}
}

// TestSafariSendsNeither —— Safari 两样都不发。
func TestSafariSendsNeither(t *testing.T) {
	h := browserHeaders(transport.FamilySafari, "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Version/16.0 Safari/605.1.15")
	for k := range h {
		if strings.HasPrefix(k, "sec-ch-ua") || strings.HasPrefix(k, "sec-fetch") {
			t.Errorf("Safari 带上了 %s", k)
		}
	}
	if h["accept-language"] == "" {
		t.Error("accept-language 是所有浏览器都发的")
	}
}

// TestNoClientHintsWhenVersionUnknown —— UA 里没有版本号就不发 sec-ch-ua。
// 发一个版本对不上 UA 的，比不发更糟。
func TestNoClientHintsWhenVersionUnknown(t *testing.T) {
	if got := browserHeaders(transport.FamilyChrome, "SomethingWithoutAVersion/1")["sec-ch-ua"]; got != "" {
		t.Errorf("认不出版本却发了 sec-ch-ua: %s", got)
	}
}

func TestDecorateDoesNotMutateRequest(t *testing.T) {
	// RoundTripper 的契约是不得修改传进来的 Request。就地改的话，换身份重试时
	// 第二个身份会带着第一个身份的头出去。
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/x", nil)
	req.Header.Set("Accept", "application/json")

	fp := &transport.Fingerprint{Name: "t", Family: transport.FamilyChrome}
	out := decorate(req, fp, "Chrome/133.0.0.0")

	if req.Header.Get("User-Agent") != "" {
		t.Error("原始请求被改了")
	}
	if out.Header.Get("User-Agent") != "Chrome/133.0.0.0" {
		t.Error("克隆的请求上没有 UA")
	}
	if out.Header.Get("Accept") != "application/json" {
		t.Error("调用方设的 Accept 被动了")
	}
}

func TestDecorateKeepsCallerHeaders(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/x", nil)
	req.Header["sec-ch-ua"] = []string{"调用方自己定的"}
	req.Header.Set("Accept-Language", "zh-CN")

	fp := &transport.Fingerprint{Name: "t", Family: transport.FamilyChrome}
	out := decorate(req, fp, "Chrome/133.0.0.0")

	if got := out.Header["sec-ch-ua"]; len(got) != 1 || got[0] != "调用方自己定的" {
		t.Errorf("直接写 map 设的头被覆盖了: %v", got)
	}
	// 用 Set 设的（规范化键名 Accept-Language）也不该被小写那份覆盖成两条。
	if n := len(out.Header.Values("Accept-Language")); n != 1 {
		t.Errorf("Accept-Language 有 %d 条，写重了", n)
	}
}

// TestLowercaseHeaderNames —— 头名的大小写本身是指纹的一部分：真 Chrome 在
// HTTP/1.1 上发的是小写 sec-ch-ua，Header.Set 会规范化成 Sec-Ch-Ua。
func TestLowercaseHeaderNames(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/x", nil)
	fp := &transport.Fingerprint{Name: "t", Family: transport.FamilyChrome}
	out := decorate(req, fp, "Chrome/133.0.0.0")

	if _, ok := out.Header["sec-ch-ua"]; !ok {
		t.Fatalf("sec-ch-ua 不是原样小写的键: %v", out.Header)
	}
	if _, ok := out.Header["Sec-Ch-Ua"]; ok {
		t.Error("头名被规范化成了 Sec-Ch-Ua")
	}
}

func TestNeverSetsAcceptEncoding(t *testing.T) {
	// 手工设 Accept-Encoding 会关掉 Go 的自动解压，而真 Chrome 报的 br 我们不会解：
	// body 变成一段无法 json.Unmarshal 的二进制，症状是"站点改了返回格式"。
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/x", nil)
	fp := &transport.Fingerprint{Name: "t", Family: transport.FamilyChrome}
	if got := decorate(req, fp, "Chrome/133").Header.Get("Accept-Encoding"); got != "" {
		t.Fatalf("Accept-Encoding 被设成了 %q", got)
	}
}

// ── 建池 ──────────────────────────────────────────────────────────────────

func TestBuildPersonasPairsByIndex(t *testing.T) {
	fps := []*transport.Fingerprint{
		{Name: "a", Family: transport.FamilyChrome, UserAgents: []string{"UA-A-win", "UA-A-mac"}},
		{Name: "b", Family: transport.FamilyChrome, UserAgents: []string{"UA-B-win"}},
	}
	proxies := []*proxyEndpoint{nil, nil, nil}

	// 3 个出口 × 2 个指纹 = 3 个身份，不是 6 个：笛卡尔积会让同一个出口 IP
	// 以多个指纹出现，那正是这个包想避免的自相矛盾。
	got, reserve := assemble(fps, proxies, false)
	if len(reserve) != 0 {
		t.Errorf("指纹比出口少，不该有备用身份，得到 %d 个", len(reserve))
	}
	if len(got) != 3 {
		t.Fatalf("身份数 = %d, 想要 3", len(got))
	}
	// UA 跟着下标走：第 0 个身份用指纹 a 的第 0 条 UA，第 1 个用 b 的第 1 条
	// （只有一条，取模回到 0），第 2 个用 a 的第 0 条……
	if got[0].ua != "UA-A-win" {
		t.Errorf("第 0 个身份的 UA = %q", got[0].ua)
	}
	if got[1].ua != "UA-B-win" {
		t.Errorf("第 1 个身份的 UA = %q", got[1].ua)
	}
}

// TestSingleExitGetsSingleIdentity —— 没有代理时默认只建一个身份。
//
// 轮换的价值在于让对方以为是不同的人，而"不同的人"首先意味着不同的 IP。
// 同一个 IP 上出现好几个浏览器身份、各自每几分钟查同一批东西，比一个稳定的身份
// **更**像程序 —— 真实世界里同一个出口后面的人不会做一模一样的事。
func TestSingleExitGetsSingleIdentity(t *testing.T) {
	ps, reserve, err := buildPersonas(&Config{}) // 无代理、无 profiles
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 {
		t.Fatalf("单出口建了 %d 个常备身份，想要 1", len(ps))
	}
	// 本测试进程没有注册任何指纹，所以剩下的可用指纹是 0 个，备用也就是 0 个。
	// 备用身份该有几个由 TestReservesAreTheLeftoverFingerprints 验。
	if len(reserve) != 0 {
		t.Fatalf("没有多余的指纹却建了 %d 个备用身份", len(reserve))
	}
}

// TestMultipleProxiesGetMultipleIdentities —— 反过来，有几个出口就该有几个身份。
func TestMultipleProxiesGetMultipleIdentities(t *testing.T) {
	ps, _, err := buildPersonas(&Config{
		Proxies: []string{"socks5://127.0.0.1:1080", "socks5://127.0.0.1:1081", "http://127.0.0.1:8080"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 3 {
		t.Fatalf("3 个代理建了 %d 个身份，想要 3", len(ps))
	}
}

// TestExplicitProfilesOverrideSingleIdentity —— 在单 IP 上强行轮换指纹是个需要
// 说明理由的选择，显式配 profiles 就能拿到。
func TestExplicitProfilesOverrideSingleIdentity(t *testing.T) {
	// 本测试进程没有注册任何指纹（没空导入 core/ja3），所以这里绕开配置解析、
	// 直接把假指纹喂给配对规则本身。
	fps := []*transport.Fingerprint{{Name: "a"}, {Name: "b"}}
	got, _ := assemble(fps, []*proxyEndpoint{nil}, true)
	if len(got) != 2 {
		t.Fatalf("显式两个指纹应当得到 2 个身份，得到 %d", len(got))
	}
}

func TestPreferDefaultPutsChrome133First(t *testing.T) {
	// transport.Fingerprints() 按名字排序，chrome_120 会排在 chrome_133 前面 ——
	// 而单出口时只会用到第一个，那不该是个老版本。
	in := []*transport.Fingerprint{{Name: "chrome_120"}, {Name: "chrome_133"}, {Name: "firefox_120"}}
	got := preferDefault(in)
	if got[0].Name != defaultProfile {
		t.Fatalf("第一个是 %s，想要 %s", got[0].Name, defaultProfile)
	}
	if len(got) != len(in) {
		t.Fatalf("挪动之后少了指纹: %d → %d", len(in), len(got))
	}
}

// TestProxyConfiguredButUnusableRefusesDirect 是本包最重要的一条守卫。
//
// 配了代理却一个都建不起来时**必须拒绝出站**，而不是退回本机出口 —— 会配代理的人
// 多半正是为了不让对方看见自己的 IP，静默直连把这件事悄悄反过来做了，而且没有任何
// 迹象。
func TestProxyConfiguredButUnusableRefusesDirect(t *testing.T) {
	_, _, err := buildPersonas(&Config{
		Proxies:     []string{"ftp://nope", "://也不是个URL"},
		AllowDirect: false,
	})
	if !errors.Is(err, ErrNoUsableProxy) {
		t.Fatalf("err = %v, 想要 ErrNoUsableProxy", err)
	}
}

func TestProxyUnusableFallsBackOnlyWhenAllowed(t *testing.T) {
	ps, _, err := buildPersonas(&Config{Proxies: []string{"ftp://nope"}, AllowDirect: true})
	if err != nil {
		t.Fatalf("allow_direct=true 时不该失败: %v", err)
	}
	for _, p := range ps {
		if p.out != nil {
			t.Errorf("身份 %s 应该是直连", p.label)
		}
	}
}

func TestSocks5ProxyIsAccepted(t *testing.T) {
	ps, err := resolveProxies(&Config{Proxies: []string{"socks5://127.0.0.1:1080"}})
	if err != nil || len(ps) != 1 || ps[0] == nil {
		t.Fatalf("socks5 代理没被建成出口: %v", err)
	}
}

// TestUnknownProfileIsSkippedLoudly —— 指纹名写错不该静默跳过：
// "我配了 3 个指纹"与"实际在用 1 个"之间必须有可见的差别。
func TestUnknownProfileIsSkippedLoudly(t *testing.T) {
	// 没有注册任何指纹时（本测试进程就是），配了名字也只能退回"不伪装"。
	fps := resolveFingerprints(&Config{Profiles: []string{"chrome133"}})
	if len(fps) != 1 || fps[0] != nil {
		t.Fatalf("没有可用指纹时应当退回不伪装，得到 %+v", fps)
	}
}

// TestNoFingerprintsDegradesToPlainTLS —— 没空导入 core/ja3 时仍然能正常出站
// （只是不伪装）。这条路必须走得通：核心业务不该因为可选依赖缺席而挂掉。
func TestNoFingerprintsDegradesToPlainTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	p := newPersona(nil, 0, nil)
	p.h1.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	cli := &http.Client{Timeout: 10 * time.Second, Transport: testRotator(p)}
	rsp, err := cli.Get(srv.URL)
	if err != nil {
		t.Fatalf("不伪装这条路走不通: %v", err)
	}
	defer rsp.Body.Close()
	if rsp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP %d", rsp.StatusCode)
	}
}

func TestRedactProxyHidesCredentials(t *testing.T) {
	got := RedactProxy("http://alice:s3cr3t@gw.example.com:8080")
	if strings.Contains(got, "s3cr3t") {
		t.Fatalf("日志串里还带着密码: %s", got)
	}
	if !strings.Contains(got, "gw.example.com:8080") {
		t.Fatalf("脱敏过头了: %s", got)
	}
}

// ── 轮换与冷却 ────────────────────────────────────────────────────────────

func TestRotatorRoundRobin(t *testing.T) {
	r := testRotatorN(3)
	seen := map[string]int{}
	for i := 0; i < 6; i++ {
		seen[r.pick().label]++
	}
	if len(seen) != 3 {
		t.Fatalf("6 次取到 %d 个不同身份，想要 3", len(seen))
	}
	for label, n := range seen {
		if n != 2 {
			t.Errorf("身份 %s 被取了 %d 次，想要 2", label, n)
		}
	}
}

func TestRotatorSkipsCooling(t *testing.T) {
	r := testRotatorN(2)
	r.personas[0].markFailure(time.Minute)

	for i := 0; i < 4; i++ {
		if got := r.pick(); got != r.personas[1] {
			t.Fatalf("第 %d 次取到了冷却中的身份", i+1)
		}
	}

	r.personas[0].markSuccess() // 一次成功即完全恢复
	seen := map[*persona]bool{}
	for i := 0; i < 4; i++ {
		seen[r.pick()] = true
	}
	if len(seen) != 2 {
		t.Fatal("markSuccess 之后该身份没有回到轮转里")
	}
}

// TestRotatorFailsOpen —— 一个把 100% 出口都摘掉的熔断器，比它要防的故障更糟。
func TestRotatorFailsOpen(t *testing.T) {
	r := testRotatorN(2)
	r.personas[0].markFailure(time.Hour)
	r.personas[1].markFailure(time.Minute)

	got := r.pick()
	if got == nil {
		t.Fatal("全部冷却时返回了 nil")
	}
	if got != r.personas[1] {
		t.Error("没挑冷却最早到期的那个")
	}
}

func TestCooldownBacksOffAndCaps(t *testing.T) {
	p := newPersona(nil, 0, nil)
	var last time.Duration
	for i := 0; i < 12; i++ {
		before := time.Now()
		p.markFailure(30 * time.Second)
		d := p.cooldownEnd().Sub(before)
		if d > cooldownMax+time.Second {
			t.Fatalf("第 %d 次的冷却 %v 超过上限", i+1, d)
		}
		// 容差 1 秒：封顶之后每次都是 cooldownMax，两次测量只差在读时钟上。
		if i > 0 && d+time.Second < last {
			t.Fatalf("第 %d 次冷却 %v 比上一次 %v 还短", i+1, d, last)
		}
		last = d
	}
	if last < cooldownMax {
		t.Fatalf("连续失败 12 次后冷却只有 %v", last)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────

func testRotatorN(n int) *rotator {
	ps := make([]*persona, 0, n)
	for i := 0; i < n; i++ {
		p := newPersona(nil, i, nil)
		p.label = string(rune('A' + i))
		ps = append(ps, p)
	}
	return testRotator(ps...)
}

// testRotator 把 once 先消耗掉，好让 RoundTrip 用给定的身份而不去读配置文件
// （go test 下 config.AppPath 指向可执行文件的临时目录，读不到 config.json）。
func testRotator(ps ...*persona) *rotator {
	r := &rotator{personas: ps}
	r.once.Do(func() {})
	return r
}
