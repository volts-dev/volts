package ja3

import (
	"fmt"
	"sort"
	"strings"

	utls "github.com/refraction-networking/utls"
	"github.com/volts-dev/volts/transport"
)

// 预置指纹 —— **比 JA3 串强得多的那条路**。
//
// # 为什么 JA3 串不够
//
// JA3 只记录五样东西：TLS 版本、密码套件 id、**扩展 id 列表**、椭圆曲线、点格式。
// 它**不记录扩展的内容**。而现代浏览器的 ClientHello 里，认得出身份的东西恰恰在
// 内容里：
//
//   - **GREASE**（RFC 8701）：Chrome 在密码套件、扩展、曲线、supported_versions 里
//     各塞一个随机的保留值。JA3 工具通常把它们过滤掉，于是串里根本没有这一维。
//   - **扩展乱序**：Chrome 106 起每条连接都把扩展**打乱**顺序。JA3 是按顺序拼的，
//     所以一个真 Chrome 每次连接的 JA3 都不同 —— 一个固定的 JA3 串反倒是"不是
//     Chrome"的证据。
//   - **后量子 key share**（X25519MLKEM768）：Chrome 124 起默认发，是一大坨字节，
//     JA3 里只表现为曲线列表里多一个 id。
//   - **ALPS**（17513）、**ECH**（65037）：内容是协议名与加密载荷，JA3 只记 id。
//
// 所以从 JA3 串重建 ClientHello 永远只是**近似**。近似的结果不是"像 Chrome"，
// 而是"一个谁都不是的客户端" —— 那比 Go 默认的指纹还独特，因为世界上只有你在发。
//
// # 预置指纹是逐字节精确的
//
// utls 直接维护了这些浏览器的 ClientHello 原样（`u_parrots.go`），GREASE、乱序、
// 后量子 key share、ALPS 全都在。用名字选一个，比抄一串 JA3 数字**又准又短**。
//
//	ja3.Dial(conn, host, cfg, "chrome_133")   // 而不是那串 200 字符的数字
//
// # UA 与指纹的配对不该靠人抄
//
// "TLS 指纹说是 Chrome 98、UA 说是 Chrome 140"是 bot 检测最先查的那一条，而它
// 100% 来自人手抄错。所以每个 profile 自带**与之自洽的 UA**（见 UserAgents），
// 调用方不必知道哪个版本配哪个字符串。
//
// Family 同理：Firefox 与 Safari **不发** `sec-ch-ua` 那几个 client hints，
// 只有 Chromium 系才发。用 Firefox 的 TLS 指纹却带上 sec-ch-ua，是一句立刻穿帮的
// 自我介绍。HTTP 层据此分流（见 Family）。

type (
	// Profile 把一个 utls 预置 ClientHello 与它的元数据绑在一起。
	//
	// **元数据（名字/族/UA）住在 volts/transport 里**，不在本包 —— 那些数据不需要
	// utls，而上层要读它们来配 UA、决定发不发 client hints。详见
	// transport/fingerprint.go 的注释。本包只多握着一样东西：怎么把这个名字变成
	// 一个 ClientHello。
	Profile struct {
		*transport.Fingerprint

		// id 是 utls 的预置 ClientHello。
		id utls.ClientHelloID
	}
)

// Family 常量转发自 transport —— 那里是它们的定义处，因为按族分流 HTTP 行为的
// 是上层，而上层不该为了一个字符串常量去依赖 utls。
const (
	FamilyChrome  = transport.FamilyChrome
	FamilyFirefox = transport.FamilyFirefox
	FamilySafari  = transport.FamilySafari
	FamilyEdge    = transport.FamilyEdge
	FamilyIOS     = transport.FamilyIOS
)

// profiles 是可用的预置指纹：名字 → utls 的 ClientHello。
//
// ★ 每加一条都要有 TestProfilesHandshake 兜着：utls 的某些预置与当前版本的库
// **不兼容**（HelloEdge_106 就是，utls 自己的注释写着 "seems to be incompatible
// with this library"），而不兼容的表现是握手失败 —— 与"网络不通"分不开。
var profiles = map[string]*Profile{}

// register 登记一条预置，并把它的元数据推给 transport（上层从那里读，不必依赖
// 本包，见 transport/fingerprint.go）。
func register(id utls.ClientHelloID, fp *transport.Fingerprint) *Profile {
	if _, dup := profiles[fp.Name]; dup {
		panic("ja3: 预置指纹 " + fp.Name + " 登记了两次")
	}
	p := &Profile{Fingerprint: fp, id: id}
	profiles[fp.Name] = p
	transport.RegisterFingerprint(fp)
	return p
}

// alias 让一个短名指向已登记的指纹。
//
// ★ 别名指向的是一个**具体版本**，不是 utls 的 HelloChrome_Auto。Auto 会随 utls
// 升级而改变，于是升一次依赖，出站身份在对方眼里就换了一个人 —— 而这件事没有
// 任何地方会提示你。要跟新版就显式地跟。
func alias(name, target string) {
	p, ok := profiles[target]
	if !ok {
		panic("ja3: 别名 " + name + " 指向了不存在的 " + target)
	}
	profiles[name] = p
	transport.RegisterFingerprint(&transport.Fingerprint{
		Name:       name,
		Family:     p.Family,
		UserAgents: p.UserAgents,
		Alias:      true,
	})
}

func init() {
	// ── Chrome ────────────────────────────────────────────────────────────
	//
	// ★ UA 里的次版本号是 `0.0.0` 不是笔误：Chrome 107 起做了 UA Reduction，
	// 真实浏览器发的就是 `Chrome/133.0.0.0`。填一个"看起来更具体"的
	// `133.0.6943.98` 反而不对 —— 那是 2022 年之前的写法。
	// macOS 版本同样被冻结成 `10_15_7`，无论真实系统是哪一版。
	register(utls.HelloChrome_133, &transport.Fingerprint{
		Name: "chrome_133", Family: FamilyChrome,
		UserAgents: []string{
			"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36",
			"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36",
			"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36",
		},
	})
	register(utls.HelloChrome_131, &transport.Fingerprint{
		Name: "chrome_131", Family: FamilyChrome,
		UserAgents: []string{
			"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
			"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		},
	})
	register(utls.HelloChrome_120, &transport.Fingerprint{
		Name: "chrome_120", Family: FamilyChrome,
		UserAgents: []string{
			"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
			"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		},
	})

	// ── Firefox ───────────────────────────────────────────────────────────
	//
	// ★ Firefox **不发** sec-ch-ua（client hints 是 Chromium 的东西）。
	register(utls.HelloFirefox_120, &transport.Fingerprint{
		Name: "firefox_120", Family: FamilyFirefox,
		UserAgents: []string{
			"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:120.0) Gecko/20100101 Firefox/120.0",
			"Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:120.0) Gecko/20100101 Firefox/120.0",
			"Mozilla/5.0 (X11; Linux x86_64; rv:120.0) Gecko/20100101 Firefox/120.0",
		},
	})

	// ── Safari ────────────────────────────────────────────────────────────
	//
	// ★ Safari 只有 macOS 一个平台 —— 一个 Safari 的 TLS 指纹配 Windows 的 UA
	// 是不可能存在的组合。
	register(utls.HelloSafari_16_0, &transport.Fingerprint{
		Name: "safari_16", Family: FamilySafari,
		UserAgents: []string{
			"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.0 Safari/605.1.15",
		},
	})
	register(utls.HelloIOS_14, &transport.Fingerprint{
		Name: "ios_14", Family: FamilyIOS,
		UserAgents: []string{
			"Mozilla/5.0 (iPhone; CPU iPhone OS 14_7_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/14.1.2 Mobile/15E148 Safari/604.1",
		},
	})

	alias("chrome", "chrome_133")
	alias("firefox", "firefox_120")
	alias("safari", "safari_16")
}

// LookupProfile 按名字取预置指纹。名字大小写不敏感。
func LookupProfile(name string) (*Profile, bool) {
	p, ok := profiles[strings.ToLower(strings.TrimSpace(name))]
	return p, ok
}

// Profiles 返回全部预置名（含别名），已排序。用于配置报错时把可选项列出来 ——
// 一个写错的名字（"chrome133" 少了下划线）应该当场看见能写什么。
func Profiles() []string {
	out := make([]string, 0, len(profiles))
	for name := range profiles {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Spec 返回这个指纹的 ClientHelloSpec，用于诊断与测试（"这个 profile 到底发了
// 什么扩展"）。生产路径不需要它 —— Dial 直接把 ClientHelloID 交给 utls。
func (p *Profile) Spec() (utls.ClientHelloSpec, error) {
	return utls.UTLSIdToSpec(p.id)
}

func (p *Profile) String() string { return p.Name }

// unknownProfileError 在名字查不到时把可选项列出来。
func unknownProfileError(name string) error {
	return fmt.Errorf("ja3: 不认识的指纹 %q —— 可用的预置有 %s；要用 JA3 串请写完整的五段（TLSVersion,Ciphers,Extensions,Curves,PointFormats）",
		name, strings.Join(Profiles(), ", "))
}
