// Package accept_language 是 Accept-Language 协商的**唯一实现**。
//
// 语言协商在这个项目里曾经有两份:本包(只取最高 q 值的原始标签)和上层 website 模块
// 自己手写的一份(连 parseFloat 都自己搓了一个)。两份的匹配语义还不一样,同一个
// Accept-Language 在前台页面和后台会话里选出不同的语言。所以这里把它收成纯函数
// (Parse/Negotiate 不碰 router、不碰全局态),上层一律调这里。
//
// 匹配语义按 RFC 4647 的 Lookup:**偏好顺序优先于精确度**。
// 例:`Accept-Language: zh-TW, en` 而我们支持 [zh_CN, en_US] —— 选 zh_CN,
// 因为用户把中文排在英文前面,给他简体中文比给英文更接近意图。
package accept_language

import (
	"sort"
	"strconv"
	"strings"

	"github.com/volts-dev/volts/router"
)

type (
	TLanguage struct {
		Language string
		Quality  float32
	}

	AcceptLanguages []TLanguage

	// TAcceptLanguage 供控制器以 DI 内嵌方式取用协商能力(见 router 的
	// RegisterMiddleware/内嵌绑定约定)。Handler 刻意为空:本类型不往请求链上
	// 加任何行为,它只是一个方法宿主。真正的逻辑在包级纯函数里。
	TAcceptLanguage struct {
	}
)

func NewAcceptLanguage() router.IMiddleware {
	return &TAcceptLanguage{}
}

func (self *TAcceptLanguage) Handler(hd router.IContext) {
}

// Parse 解析 Accept-Language 头为按 q 值降序的偏好列表。
//
// 三条容易写错的规则:
//   - **q=0 是显式拒绝,必须剔除**。`de, en;q=0` 的意思是"要德语,而且绝不要英语";
//     把 en 留在列表里,当支持语言只有 en 时就会回一个客户端刚拒绝掉的语言。
//   - q 不可解析时按 1 处理,而不是丢弃整个头。头是客户端给的,一个坏浮点不该
//     让整串偏好失效。
//   - 排序必须稳定。同 q 值的相对顺序在 RFC 里没规定,但客户端惯例是按优先级
//     书写,打乱它等于丢掉信息。
func Parse(header string) AcceptLanguages {
	if header == "" {
		return nil
	}

	parts := strings.Split(header, ",")
	out := make(AcceptLanguages, 0, len(parts))
	for _, part := range parts {
		tag := strings.TrimSpace(part)
		if tag == "" {
			continue
		}
		q := float32(1)
		// 参数段(;q=0.8、;charset=... 之类)一律从第一个分号切开,只认 q。
		if i := strings.IndexByte(tag, ';'); i >= 0 {
			params := tag[i+1:]
			tag = strings.TrimSpace(tag[:i])
			if tag == "" {
				continue
			}
			for _, p := range strings.Split(params, ";") {
				p = strings.TrimSpace(p)
				if !strings.HasPrefix(strings.ToLower(p), "q=") {
					continue
				}
				if f, err := strconv.ParseFloat(strings.TrimSpace(p[2:]), 32); err == nil {
					q = float32(f)
				}
				break
			}
		}
		if q <= 0 {
			continue // 显式拒绝
		}
		out = append(out, TLanguage{Language: tag, Quality: q})
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Quality > out[j].Quality })
	return out
}

// Negotiate 在 supported 里挑与 header 最匹配的一门语言,返回 **supported 里的原样
// 元素**(调用方拿到的是自己那套写法 "zh_CN",而不是归一化后的 "zh-cn"),无匹配返回 ""。
//
// 对每个偏好,按标签逐级截断(zh-Hant-TW → zh-Hant → zh)找,每级里:
//  1. 先找完全相等的;
//  2. 再找 supported 中更具体的(偏好 zh 命中 zh_CN)——浏览器常只发基语言,
//     而我们支持的是带地区的。
//
// 第 1、2 步的次序是必须的:偏好 zh-TW 而 supported=[zh_CN, zh_TW] 时,若不先做
// 精确匹配就会按 supported 的存放顺序命中 zh_CN——把繁体用户丢进简体界面。
//
// `*` 表示接受任意语言,返回 supported 首项(调用方应把默认语言放在首位)。
func Negotiate(header string, supported []string) string {
	if len(supported) == 0 {
		return ""
	}

	norm := make([]string, len(supported))
	for i, s := range supported {
		norm[i] = normalize(s)
	}

	for _, pref := range Parse(header) {
		tag := normalize(pref.Language)
		if tag == "*" {
			return supported[0]
		}
		for tag != "" {
			for i := range norm {
				if norm[i] == tag {
					return supported[i]
				}
			}
			// 子标签边界前缀:tag="zh" 命中 "zh-cn",但不能命中 "zho"。
			for i := range norm {
				if strings.HasPrefix(norm[i], tag+"-") {
					return supported[i]
				}
			}
			i := strings.LastIndexByte(tag, '-')
			if i < 0 {
				break
			}
			tag = tag[:i]
		}
	}
	return ""
}

// Detect 从请求头协商语言,等价于 Negotiate(请求的 Accept-Language, supported)。
func Detect(hd *router.THttpContext, supported []string) string {
	if hd == nil {
		return ""
	}
	return Negotiate(hd.Request().Header().Get("Accept-Language"), supported)
}

// NearestLang 返回 q 值最高的原始语言标签,不做任何匹配。保留给已有调用方。
//
// 注意它**不判断我们是否支持这门语言**——新代码请用 Negotiate/Detect,
// 传入支持语言集,否则拿到的可能是一个我们根本没有译文的标签。
func (self *TAcceptLanguage) NearestLang(hd *router.THttpContext) string {
	if hd == nil {
		return ""
	}
	langs := Parse(hd.Request().Header().Get("Accept-Language"))
	if len(langs) == 0 {
		return ""
	}
	return langs[0].Language
}

// normalize 把语言标签归一为小写连字符形式:zh_CN / zh-CN → zh-cn。
// 项目里 res.lang.code 用下划线(zh_CN)、HTTP 头和 BCP47 用连字符(zh-CN),
// 比较前必须统一,否则永远匹配不上。
func normalize(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "_", "-")
}
