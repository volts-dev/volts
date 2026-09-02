package ja3

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	utls "github.com/refraction-networking/utls"
)

// TestProfilesHandshake —— 每个预置指纹都要能真的握上手。
//
// utls 的某些预置与当前版本的库**不兼容**（它自己的注释就写着 HelloEdge_106
// "seems to be incompatible with this library"），而不兼容的表现是握手失败 ——
// 在调用方那里与"网络不通"分不开。登记一个握不了手的 profile，等于给配置文件里
// 放了一个必然失败的选项。
func TestProfilesHandshake(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	for _, name := range Profiles() {
		t.Run(name, func(t *testing.T) {
			conn := dialTest(t, srv.Listener.Addr().String(), name)
			defer conn.Close()

			state := conn.(*uconn).ConnectionState()
			if state.Version < tls.VersionTLS12 {
				t.Errorf("协商到了 TLS 0x%04x —— 低于 1.2", state.Version)
			}
			t.Logf("%s: TLS 0x%04x, 套件 0x%04x, ALPN %q",
				name, state.Version, state.CipherSuite, state.NegotiatedProtocol)
		})
	}
}

// TestModernProfilesNegotiateTLS13 —— 现代 Chrome/Firefox 一定协商 TLS 1.3。
//
// 这条是"指纹到底新不新"的判据：一个声称是 Chrome 133 却只会 TLS 1.2 的客户端，
// 在任何做指纹的地方都是立刻穿帮的组合。
func TestModernProfilesNegotiateTLS13(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	for _, name := range []string{"chrome_133", "chrome_131", "chrome_120", "firefox_120"} {
		t.Run(name, func(t *testing.T) {
			conn := dialTest(t, srv.Listener.Addr().String(), name)
			defer conn.Close()

			if got := conn.(*uconn).ConnectionState().Version; got != tls.VersionTLS13 {
				t.Errorf("协商到 TLS 0x%04x，想要 1.3（0x%04x）", got, tls.VersionTLS13)
			}
		})
	}
}

// TestChrome133HasModernSignals —— 预置指纹带着 JA3 串表达不了的那几样。
//
// 它们正是"用预置名而不是 JA3 串"的全部理由；哪天 utls 升级把它们弄丢了，
// 这条会说话，而线上只会表现为"最近好像更容易被挡了"。
func TestChrome133HasModernSignals(t *testing.T) {
	p, ok := LookupProfile("chrome_133")
	if !ok {
		t.Fatal("chrome_133 没登记")
	}
	spec, err := p.Spec()
	if err != nil {
		t.Fatal(err)
	}

	var hasGREASE, hasALPS, hasPQ bool
	for _, ext := range spec.Extensions {
		switch e := ext.(type) {
		case *utls.UtlsGREASEExtension:
			hasGREASE = true
		case *utls.ApplicationSettingsExtension, *utls.ApplicationSettingsExtensionNew:
			hasALPS = true
		case *utls.SupportedCurvesExtension:
			for _, c := range e.Curves {
				if c == utls.X25519MLKEM768 {
					hasPQ = true
				}
			}
		}
	}

	if !hasGREASE {
		t.Error("没有 GREASE —— JA3 串里滤掉的那一维")
	}
	if !hasALPS {
		t.Error("没有 ALPS（17513）")
	}
	if !hasPQ {
		t.Error("没有后量子曲线 X25519MLKEM768 —— Chrome 124 起默认发")
	}
}

// TestProfileUserAgentsMatchFamily —— UA 与指纹必须自洽。
//
// "TLS 指纹说是 Chrome、UA 说是 Firefox"是 bot 检测最先查的那一条，而它 100% 来自
// 人手抄。这条用例把那件事挡在提交之前。
func TestProfileUserAgentsMatchFamily(t *testing.T) {
	want := map[string]string{
		FamilyChrome:  "Chrome/",
		FamilyFirefox: "Firefox/",
		FamilySafari:  "Safari/",
		FamilyIOS:     "Mobile/",
	}

	for _, name := range Profiles() {
		p, _ := LookupProfile(name)
		agents := p.UserAgents
		if len(agents) == 0 {
			t.Errorf("%s 一条 UA 都没有", name)
			continue
		}
		marker, known := want[p.Family]
		if !known {
			t.Errorf("%s 的 Family %q 没有对应的 UA 判据", name, p.Family)
			continue
		}
		for _, ua := range agents {
			if !strings.Contains(ua, marker) {
				t.Errorf("%s 的 UA 里没有 %q: %s", name, marker, ua)
			}
		}

		// 版本号也要对上：chrome_120 配 Chrome/133 的 UA 是同一类错误的另一半。
		if p.Family == FamilyChrome || p.Family == FamilyFirefox {
			if ver := versionSuffix(name); ver != "" {
				for _, ua := range agents {
					if !strings.Contains(ua, "/"+ver+".") {
						t.Errorf("%s 的 UA 版本对不上: %s", name, ua)
					}
				}
			}
		}
	}
}

// TestFirefoxAndSafariAreNotChrome —— Family 是 HTTP 层决定发不发 client hints
// 的唯一依据。标错一个字母，Firefox 就会带上 sec-ch-ua 出门。
func TestFirefoxAndSafariAreNotChrome(t *testing.T) {
	for name, wantFamily := range map[string]string{
		"chrome_133":  FamilyChrome,
		"firefox_120": FamilyFirefox,
		"safari_16":   FamilySafari,
		"ios_14":      FamilyIOS,
	} {
		p, ok := LookupProfile(name)
		if !ok {
			t.Fatalf("%s 没登记", name)
		}
		if p.Family != wantFamily {
			t.Errorf("%s 的 Family = %q, 想要 %q", name, p.Family, wantFamily)
		}
	}
}

// TestAliasesArePinned —— 别名必须指向一个**具体版本**，不是 utls 的 *_Auto。
//
// Auto 会随 utls 升级改变，于是升一次依赖，出站身份在对方眼里就换了一个人 ——
// 而这件事没有任何地方会提示你。
func TestAliasesArePinned(t *testing.T) {
	for alias, want := range map[string]string{
		"chrome":  "chrome_133",
		"firefox": "firefox_120",
		"safari":  "safari_16",
	} {
		a, ok := LookupProfile(alias)
		if !ok {
			t.Fatalf("别名 %s 没登记", alias)
		}
		w, _ := LookupProfile(want)
		if a != w {
			t.Errorf("别名 %s 指向 %s，想要 %s", alias, a.Name, want)
		}
	}
}

func TestLookupProfileIsCaseInsensitive(t *testing.T) {
	for _, name := range []string{"Chrome_133", " chrome_133 ", "CHROME_133"} {
		if _, ok := LookupProfile(name); !ok {
			t.Errorf("%q 查不到", name)
		}
	}
}

// TestUnknownSpecNamesTheOptions —— 写错一个下划线不该表现为一个看不懂的解析失败。
func TestUnknownSpecNamesTheOptions(t *testing.T) {
	rawConn, done := deadConn(t)
	defer done()

	_, err := Dial(rawConn, "example.com", nil, "chrome133")
	if err == nil {
		t.Fatal("不认识的名字被接受了")
	}
	if !strings.Contains(err.Error(), "chrome_133") {
		t.Errorf("错误信息里没有列出可用的预置名: %v", err)
	}
}

// ── JA3 串这条路 ──────────────────────────────────────────────────────────

// TestJA3StringStillWorks —— 向后兼容：老配置里那些串还得能用。
func TestJA3StringStillWorks(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	// connect_components 与本包集成测试里用过的三条。
	for _, ja3 := range []string{
		"771,4866-4867-4865-49196-49200-159-52393-52392-52394-49195-49199-158-49188-49192-107-49187-49191-103-49162-49172-57-49161-49171-51-157-156-61-60-53-47-255,0-11-10-35-16-22-23-13-43-45-51-21,29-23-30-25-24,0-1-2",
		"771,4865-4866-4867-49195-49199-49196-49200-52393-52392-49171-49172-156-157-47-53,0-23-65281-10-11-35-16-5-13-18-51-45-43-27-21,29-23-24,0",
	} {
		conn := dialTest(t, srv.Listener.Addr().String(), ja3)
		state := conn.(*uconn).ConnectionState()
		t.Logf("TLS 0x%04x 套件 0x%04x", state.Version, state.CipherSuite)
		conn.Close()
	}
}

// TestJA3WithSupportedVersionsGetsTLS13 —— 串里有扩展 43 就该协商 TLS 1.3。
//
// 这里曾经硬编码成 1.2/1.1/1.0：每一条 JA3 串重建出来的都是一个"只会 TLS 1.2 的
// 浏览器"，而 2016 年之后的任何一版 Chrome 都协商 1.3。
func TestJA3WithSupportedVersionsGetsTLS13(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	const withExt43 = "771,4865-4866-4867-49195-49199,0-23-65281-10-11-35-16-13-51-45-43,29-23-24,0"
	conn := dialTest(t, srv.Listener.Addr().String(), withExt43)
	defer conn.Close()

	if got := conn.(*uconn).ConnectionState().Version; got != tls.VersionTLS13 {
		t.Errorf("协商到 TLS 0x%04x，想要 1.3", got)
	}
}

// TestJA3WithoutSupportedVersionsStaysTLS12 —— 反过来也要成立：一条真正的
// TLS 1.2 客户端指纹（没有扩展 43）不该被我们"升级"成 1.3。
func TestJA3WithoutSupportedVersionsStaysTLS12(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	const noExt43 = "771,49195-49199-49196-49200-52393-52392-49171-49172-156-157-47-53,0-23-65281-10-11-35-16-5-13-18,29-23-24,0"
	conn := dialTest(t, srv.Listener.Addr().String(), noExt43)
	defer conn.Close()

	if got := conn.(*uconn).ConnectionState().Version; got != tls.VersionTLS12 {
		t.Errorf("协商到 TLS 0x%04x，想要 1.2", got)
	}
}

func TestKeyShareFollowsCurves(t *testing.T) {
	// 一条只声称支持 X25519 的串，key_share 里就该只有 X25519（外加 GREASE）——
	// 为每条曲线都生成一份密钥是任何真实浏览器都不会做的事。
	spec, err := parseJA3("771,4865,0-43-51-10-11,29,0")
	if err != nil {
		t.Fatal(err)
	}
	for _, ext := range spec.Extensions {
		ks, ok := ext.(*utls.KeyShareExtension)
		if !ok {
			continue
		}
		// 串里没有 GREASE 曲线，所以 key_share 里也不该有 —— 只剩 X25519 一条。
		if len(ks.KeyShares) != 1 {
			t.Fatalf("key_share 有 %d 条，想要 1（只有 X25519）: %+v", len(ks.KeyShares), ks.KeyShares)
		}
		if ks.KeyShares[0].Group != utls.X25519 {
			t.Errorf("key_share 的曲线 = 0x%04x, 想要 X25519", uint16(ks.KeyShares[0].Group))
		}
		return
	}
	t.Fatal("重建出来的 spec 里没有 key_share")
}

// TestKeyShareIsSubsetOfSupportedGroups —— RFC 8446 §4.2.8：客户端**不得**为
// supported_groups 里没有的 group 提供 key_share。
//
// ★★ 这条必须是**结构断言**，不能靠一次成功的握手来证明：Go 的 TLS 服务端
// 不检查这一条，所以 httptest 上的绿灯什么都不说明。2026-09-02 实测，同一个
// 违规的 ClientHello 在 httptest 上握手成功、在 www.ti.com（Akamai）上回
// `illegal parameter` —— 一个既不提 key_share 也不提 supported_groups 的错误，
// 在调用方那里与"网络不通"分不开。
func TestKeyShareIsSubsetOfSupportedGroups(t *testing.T) {
	for _, ja3 := range []string{
		// 曲线段**没有** GREASE：key_share 里也不许有。这条就是当初炸掉的那个串。
		"771,4866-4867-4865-49196-49200-159-52393-52392-52394-49195-49199-158-49188-49192-107-49187-49191-103-49162-49172-57-49161-49171-51-157-156-61-60-53-47-255,0-11-10-35-16-22-23-13-43-45-51-21,29-23-1035-25-24,0-1-2",
		// 曲线段**有** GREASE（0x2a2a=10794）：key_share 里可以有，而且应该有。
		"771,4865-4866,0-10-11-43-51,10794-29-23,0",
	} {
		spec, err := parseJA3(ja3)
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}

		groups := map[utls.CurveID]bool{}
		var ks *utls.KeyShareExtension
		for _, ext := range spec.Extensions {
			switch e := ext.(type) {
			case *utls.SupportedCurvesExtension:
				for _, c := range e.Curves {
					groups[c] = true
				}
			case *utls.KeyShareExtension:
				ks = e
			}
		}
		if ks == nil {
			t.Fatal("重建出来的 spec 里没有 key_share")
		}
		for _, share := range ks.KeyShares {
			if !groups[share.Group] {
				t.Errorf("key_share 提了 0x%04x，但 supported_groups 里没有它（违反 RFC 8446 §4.2.8）\n串: %s",
					uint16(share.Group), ja3)
			}
		}
	}
}

// TestNoGREASEInvented —— 重建只能还原串里有的，不能凭空添加。
//
// 一个没有 GREASE 的 JA3 串（Firefox、老客户端、抓包工具滤过的）重建出来也不该
// 有 GREASE：凭空加出来的既不是串里描述的那个客户端，也不是任何真实浏览器。
func TestNoGREASEInvented(t *testing.T) {
	spec, err := parseJA3("771,4865-4866,0-10-11-43-51,29-23,0")
	if err != nil {
		t.Fatal(err)
	}
	for _, ext := range spec.Extensions {
		switch e := ext.(type) {
		case *utls.UtlsGREASEExtension:
			t.Error("凭空加了一个 GREASE 扩展")
		case *utls.SupportedVersionsExtension:
			for _, v := range e.Versions {
				if v == utls.GREASE_PLACEHOLDER {
					t.Error("supported_versions 里凭空加了 GREASE")
				}
			}
		case *utls.KeyShareExtension:
			for _, ks := range e.KeyShares {
				if uint16(ks.Group) == utls.GREASE_PLACEHOLDER {
					t.Error("key_share 里凭空加了 GREASE")
				}
			}
		}
	}
	for _, c := range spec.CipherSuites {
		if c == utls.GREASE_PLACEHOLDER {
			t.Error("密码套件里凭空加了 GREASE")
		}
	}
}

func TestGREASEIsRecognised(t *testing.T) {
	// 0x0a0a = 2570, 0x1a1a = 6682 —— 抓包工具有时原样保留。
	spec, err := parseJA3("771,2570-4865,6682-0-43-51-10-11,64250-29,0")
	if err != nil {
		t.Fatal(err)
	}
	if spec.CipherSuites[0] != utls.GREASE_PLACEHOLDER {
		t.Errorf("GREASE 套件没被识别: 0x%04x", spec.CipherSuites[0])
	}
	if _, ok := spec.Extensions[0].(*utls.UtlsGREASEExtension); !ok {
		t.Errorf("GREASE 扩展没被识别: %T", spec.Extensions[0])
	}
	for _, ext := range spec.Extensions {
		if c, ok := ext.(*utls.SupportedCurvesExtension); ok {
			if c.Curves[0] != utls.CurveID(utls.GREASE_PLACEHOLDER) {
				t.Errorf("GREASE 曲线没被识别: 0x%04x", uint16(c.Curves[0]))
			}
		}
	}
}

// 曲线段为空却列了 key_share —— 串自相矛盾，应当当场报错而不是造一个违规的
// ClientHello 出去（那会在真实服务器上表现为一句指不到病灶的 illegal parameter）。
func TestKeyShareWithoutCurvesIsAnError(t *testing.T) {
	if _, err := parseJA3("771,4865,0-43-51-10-11,,0"); err == nil {
		t.Fatal("空曲线段 + key_share 被接受了")
	}
}

func TestUnknownExtensionIsAnError(t *testing.T) {
	// 跳过一个不认识的扩展会得到"少了一个扩展"的指纹 —— 它不再是串里描述的那个
	// 客户端，而调用方以为自己伪装成功了。
	if _, err := parseJA3("771,4865,0-23-99999,29,0"); err == nil {
		t.Fatal("不认识的扩展 id 被静默跳过了")
	}
}

func TestMalformedJA3(t *testing.T) {
	for _, raw := range []string{
		"771,4865,0-23,29",    // 4 段
		"771,,0-23,29,0",      // 套件段空
		"771,abc,0-23,29,0",   // 套件不是数字
		"771,4865,0-23,xyz,0", // 曲线不是数字
	} {
		if _, err := parseJA3(raw); err == nil {
			t.Errorf("%q 应该被拒绝", raw)
		}
	}
}

// ── helpers ───────────────────────────────────────────────────────────────

func dialTest(t *testing.T, addr, spec string) net.Conn {
	t.Helper()

	raw, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("拨号失败: %v", err)
	}
	raw.SetDeadline(time.Now().Add(10 * time.Second))

	host, _, _ := net.SplitHostPort(addr)
	conn, err := Dial(raw, host, &tls.Config{InsecureSkipVerify: true}, spec)
	if err != nil {
		t.Fatalf("握手失败: %v", err)
	}
	conn.SetDeadline(time.Time{})
	return conn
}

// deadConn 给"根本走不到握手"的用例一条会被立刻关掉的连接。
func deadConn(t *testing.T) (net.Conn, func()) {
	t.Helper()
	c1, c2 := net.Pipe()
	return c1, func() { c1.Close(); c2.Close() }
}

// versionSuffix 从 "chrome_133" 里取出 "133"；没有版本后缀（别名）时返回 ""。
func versionSuffix(name string) string {
	i := strings.LastIndex(name, "_")
	if i < 0 {
		return ""
	}
	suffix := name[i+1:]
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return suffix
}
