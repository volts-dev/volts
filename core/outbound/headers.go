package outbound

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/volts-dev/volts/transport"
)

// HTTP 层的伪装：把一个身份的 User-Agent 与它**必然会带的那几个头**配齐。
//
// # 只做 TLS 层是半截的
//
// TLS 指纹说这是 Chrome 133，HTTP 头却只有 `Accept: application/json` 和一个
// UA —— 真 Chrome 的每一个 XHR 都带着 sec-ch-ua、sec-fetch-*、Accept-Language。
// 少了它们，一个只看 HTTP 头的检查就够了，TLS 那边做得再像也白费。
//
// # 头是**推导**出来的，不是抄的
//
// sec-ch-ua 里的版本号必须与 UA 里的一致；平台必须与 UA 里的一致。手工维护两份
// 就是在等它们对不上 —— 而"UA 说 Chrome 133、sec-ch-ua 说 Chrome 120"是一句
// 立刻穿帮的自我介绍。所以这里只有一个输入：UA 字符串，其余全部算出来。
//
// # 谁发什么
//
//	                sec-ch-ua*  sec-fetch-*  Accept-Language
//	Chromium 系        ✓            ✓             ✓
//	Firefox            ✗            ✓             ✓
//	Safari / iOS       ✗            ✗             ✓
//
// client hints 是 Chromium 的东西。给一个 Firefox 的 TLS 指纹配上 sec-ch-ua，
// 比两样都不发更容易被认出来 —— 这是 transport.Fingerprint.Family 存在的理由。

var (
	// 从 UA 里抠 Chrome/Edge 的大版本号。UA Reduction 之后次版本恒为 0.0.0，
	// 所以只有第一段有信息。
	chromeVersionRe = regexp.MustCompile(`Chrome/(\d+)`)
	edgeVersionRe   = regexp.MustCompile(`Edg/(\d+)`)
)

// decorate 按身份补齐请求头，返回一个**新的** Request。
//
// ★ 克隆再改：RoundTripper 的契约是不得修改传进来的 Request，调用方（乃至
// http.Client 的重定向逻辑）还持有它。就地改的话，换身份重试时第二个身份会带着
// 第一个身份的头出去。
//
// ★★ **只补缺的**：调用方显式设过的头一律不动（`Accept: application/json` 是
// 业务语义，不是伪装的一部分）。唯一的例外是 User-Agent —— 它是身份本身，
// 让调用方覆盖它就等于让 TLS 指纹与 UA 打架。
func decorate(req *http.Request, fp *transport.Fingerprint, ua string) *http.Request {
	if fp == nil {
		return req
	}

	out := req.Clone(req.Context())
	if ua != "" {
		out.Header.Set("User-Agent", ua)
	}
	for k, v := range browserHeaders(fp.Family, ua) {
		setIfAbsent(out.Header, k, v)
	}
	return out
}

// setIfAbsent 只在这个头**一个字都还没有**时才写。
//
// 要查两次：调用方可能用 Header.Set 写过（规范化过的键名 Sec-Ch-Ua），也可能
// 像我们一样直接写 map（小写 sec-ch-ua）。只查一种就会写出两份同名头。
func setIfAbsent(h http.Header, key, value string) {
	if _, ok := h[key]; ok {
		return
	}
	if h.Get(key) != "" {
		return
	}
	// ★ 直接写 map 而不用 Set：Set 会把 sec-ch-ua 规范化成 Sec-Ch-Ua，而
	// HTTP/1.1 上头名是原样发出去的 —— 真 Chrome 发的是小写。头名的大小写本身
	// 也是指纹的一部分。（HTTP/2 上头名一律小写，这条无所谓，但两条路得写同一份表。）
	h[key] = []string{value}
}

// browserHeaders 按浏览器族与 UA 算出这个身份必然会带的头。
func browserHeaders(family, ua string) map[string]string {
	out := map[string]string{
		// 语言是所有浏览器都发的。给一个固定值而不是随身份变：它与指纹无关，
		// 却会影响**服务端返回的内容**（价格页可能按语言给不同币种），
		// 让它随身份跳动等于让抓回来的数据随机变。
		"accept-language": "en-US,en;q=0.9",
	}

	switch family {
	case transport.FamilyChrome, transport.FamilyEdge:
		if brands := clientHintBrands(family, ua); brands != "" {
			out["sec-ch-ua"] = brands
		}
		out["sec-ch-ua-mobile"] = mobileHint(ua)
		if p := platformHint(ua); p != "" {
			out["sec-ch-ua-platform"] = p
		}
		fallthrough
	case transport.FamilyFirefox:
		// Firefox 从 90 起也发 Sec-Fetch-*（那是 Fetch Metadata 标准，不是
		// Chromium 私有的），但**不发** client hints。
		out["sec-fetch-dest"] = "empty"
		out["sec-fetch-mode"] = "cors"
		out["sec-fetch-site"] = "same-origin"
	}
	// Safari / iOS 两样都不发，只留 accept-language。
	return out
}

// clientHintBrands 拼 sec-ch-ua。
//
// 真实格式（Chrome 133 on Windows）：
//
//	"Not(A:Brand";v="99", "Google Chrome";v="133", "Chromium";v="133"
//
// ★ 那个 `Not(A:Brand` 是 **GREASE 品牌**：Chrome 故意塞一个不存在的品牌，防止
// 站点写死品牌列表。它的字面值**每几个版本换一次**（"Not A;Brand"、"Not_A Brand"、
// "Not(A:Brand"…），我们没有一张可靠的对照表。
//
// 猜一个近似值仍然远好过不发：Chrome 一定发 sec-ch-ua，**缺席**是一个所有人都能
// 查的硬信号；而 GREASE 品牌串对不上要有人专门维护一张版本对照表去比对，几乎没人做。
// 版本号那两项是从 UA 精确算出来的，永远与 UA 一致。
//
// ★ 品牌的**顺序**在真 Chrome 里是每次启动随机排的，所以顺序不是判据 ——
// 这里固定排列不会因此露馅。
func clientHintBrands(family, ua string) string {
	var version, brand string
	switch family {
	case transport.FamilyEdge:
		if m := edgeVersionRe.FindStringSubmatch(ua); m != nil {
			version, brand = m[1], "Microsoft Edge"
		}
	default:
		if m := chromeVersionRe.FindStringSubmatch(ua); m != nil {
			version, brand = m[1], "Google Chrome"
		}
	}
	if version == "" {
		// UA 里没有版本号就**不发**这个头 —— 发一个版本对不上 UA 的 sec-ch-ua
		// 比不发更糟。
		return ""
	}
	return `"Not(A:Brand";v="99", "` + brand + `";v="` + version + `", "Chromium";v="` + version + `"`
}

// platformHint 从 UA 推平台。值带引号 —— 那是 Structured Header 的字符串写法，
// 不是多余的转义。
func platformHint(ua string) string {
	switch {
	case strings.Contains(ua, "Windows NT"):
		return `"Windows"`
	case strings.Contains(ua, "Macintosh"):
		return `"macOS"`
	case strings.Contains(ua, "Android"):
		return `"Android"`
	case strings.Contains(ua, "X11; Linux"), strings.Contains(ua, "Linux x86_64"):
		return `"Linux"`
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"):
		return `"iOS"`
	}
	return ""
}

func mobileHint(ua string) string {
	if strings.Contains(ua, "Android") || strings.Contains(ua, "Mobile") {
		return "?1"
	}
	return "?0"
}
