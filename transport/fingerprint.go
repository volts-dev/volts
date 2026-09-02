package transport

import (
	"sort"
	"sync"
)

// 指纹的**元数据**注册表 —— 名字、浏览器族、与之自洽的 User-Agent。
//
// # 为什么这些数据在 transport 而不在 ja3 子模块里
//
// 拼一条 TLS 指纹需要 utls；**知道"chrome_133 配哪个 UA"不需要**。把后者留在
// 主模块里，上层（业务框架、抓取服务）就能读到全部可选身份、按族决定发不发
// client hints、把 UA 与指纹配对 —— 而**不必把 utls 写进自己的 go.mod**。
// JA3 的可选性因此一路保持到最上层，而不是只在 volts 这一层可选。
//
// 注册由 ja3 子模块在 init 里做。没有空导入它的进程，这张表是**空的** ——
// 上层据此给出精确的诊断（"要启用指纹请空导入 volts/ja3"），而不是悄悄地不伪装。
//
// # UA 为什么跟指纹绑在一起
//
// "TLS 指纹说是 Chrome 98、UA 说是 Chrome 140"是 bot 检测最先查的那一条，而它
// 100% 来自人手抄。让指纹自带 UA，调用方就没有抄错的机会。

// 浏览器族。上层靠它决定 HTTP 层的行为 —— 最要紧的是 **client hints
// （sec-ch-ua 那几个头）只有 Chromium 系才发**：给一个 Firefox 的 TLS 指纹配上
// sec-ch-ua，是一句立刻穿帮的自我介绍。
const (
	FamilyChrome  = "chrome"
	FamilyFirefox = "firefox"
	FamilySafari  = "safari"
	FamilyEdge    = "edge"
	FamilyIOS     = "ios"
)

// Fingerprint 是一个可整套使用的浏览器身份的**元数据**部分。
//
// 拼 ClientHello 的那一半在 ja3 子模块里，按 Name 对应。
type Fingerprint struct {
	// Name 是配置里写的那个名字（小写、下划线），如 "chrome_133"。
	Name string

	// Family 见上面的常量。
	Family string

	// UserAgents 是与这个 TLS 指纹自洽的 User-Agent，按平台各一条。
	//
	// TLS 指纹**不区分操作系统**（同一版 Chrome 在 mac 与 win 上 ClientHello
	// 一致），所以同一个指纹下换平台是安全的 —— 换的只是 UA 与
	// sec-ch-ua-platform。
	UserAgents []string

	// Alias 为 true 表示这是另一个指纹的别名（"chrome" → "chrome_133"）。
	// 列举可用身份时要跳过，否则同一个身份会在池子里出现两次。
	Alias bool
}

// UserAgent 返回第 i 条 UA（按需取模，i 可以是任意整数）。
//
// 给"第 n 个身份用第 n 个平台"这种轮换用；越界不报错，因为调用方的下标来自
// 身份池的编号，与本表有几条 UA 无关。
func (f *Fingerprint) UserAgent(i int) string {
	if f == nil || len(f.UserAgents) == 0 {
		return ""
	}
	if i < 0 {
		i = -i
	}
	return f.UserAgents[i%len(f.UserAgents)]
}

var (
	fpMu         sync.RWMutex
	fingerprints = map[string]*Fingerprint{}
)

// RegisterFingerprint 登记一个指纹的元数据。由 ja3 子模块在 init 中调用。
//
// 重名直接 panic：两个实现抢同一个名字，赢的那个决定所有出站请求伪装成谁 ——
// 而"身份悄悄变成了另一个"不是会有人发现的那类错误。同 codec.RegisterCodec。
func RegisterFingerprint(fp *Fingerprint) {
	if fp == nil || fp.Name == "" {
		panic("transport: RegisterFingerprint 需要一个有名字的指纹")
	}
	fpMu.Lock()
	defer fpMu.Unlock()
	if old, dup := fingerprints[fp.Name]; dup {
		panic("transport: 指纹 " + fp.Name + " 登记了两次（原 family " + old.Family + "）")
	}
	fingerprints[fp.Name] = fp
}

// LookupFingerprint 按名字取指纹元数据。
func LookupFingerprint(name string) (*Fingerprint, bool) {
	fpMu.RLock()
	defer fpMu.RUnlock()
	fp, ok := fingerprints[name]
	return fp, ok
}

// Fingerprints 返回全部**非别名**的指纹，按名字排序。
//
// 排序不是为了好看：上层拿它建身份池，map 的迭代顺序是随机的，不排序的话每次
// 重启进程身份的编号就换一遍 —— 而"第 3 个身份被限速了"这种记录会因此对不上。
func Fingerprints() []*Fingerprint {
	fpMu.RLock()
	defer fpMu.RUnlock()

	out := make([]*Fingerprint, 0, len(fingerprints))
	for _, fp := range fingerprints {
		if !fp.Alias {
			out = append(out, fp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// FingerprintNames 返回全部登记过的名字（**含别名**），已排序。
// 用于配置报错时把可选项列出来。
func FingerprintNames() []string {
	fpMu.RLock()
	defer fpMu.RUnlock()

	out := make([]string, 0, len(fingerprints))
	for name := range fingerprints {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
