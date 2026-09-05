package config

import (
	"sort"
	"strings"

	"github.com/volts-dev/utils"
)

// 未知配置项检查。
//
// 这是「改了配置没反应」最贵的来源：配置文件里把 read_timeout 写成 readtimeout、
// 把 max_registry 拼成 max_regstry，或者写了一个早已不存在的项 —— 三种情况全部
// **静默忽略**，装载返回 nil，模型保持默认值，没有任何提示。450 个 key 的文件里
// 错一个，靠肉眼是找不出来的。
//
// 检查方式是集合相减：文件里出现过的叶子路径，减去所有已注册模型声明过的字段路径，
// 差集就是没人认领的 key。对每个差集项再按编辑距离找最相近的已知项作为建议。
//
// 这也顺带把历史遗留的孤儿子树暴露出来 —— kylin 的 config.json 里同一个 client
// 同时存在 client.web.action.* 和 web.action.client.* 两份，后者是现行代码写的，
// 前者是早期版本留下的，从此无人清理也无人知道它没用。

// LintIssue 是一条未知配置项。
type LintIssue struct {
	Path       string // 配置文件里的完整路径
	Line       int    // 行号；0 表示不可得
	Suggestion string // 最相近的已知配置项；空表示没有足够相近的
}

func (self LintIssue) String() string {
	var b strings.Builder
	if self.Line > 0 {
		b.WriteString("第 ")
		b.WriteString(itoa(self.Line))
		b.WriteString(" 行: ")
	}
	b.WriteString("未知配置项 ")
	b.WriteString(quote(self.Path))
	if self.Suggestion != "" {
		b.WriteString(" —— 是否想写 ")
		b.WriteString(quote(self.Suggestion))
		b.WriteString(" ?")
	}
	return b.String()
}

func quote(s string) string { return "\"" + s + "\"" }

// Lint 报告配置文件里所有「没有任何已注册模型声明过」的配置项。
//
// 必须在全部组件装配完毕之后调用 —— 差集的正确性依赖于「所有模型都已注册」。
// service.Start() 会自动调一次；独立使用时自行掌握时机。
func (self *Config) Lint() []LintIssue {
	core := self.Core()

	known := self.knownKeys()
	if len(known) == 0 {
		// 一个模型都没注册，此时任何 key 都会被判为未知 —— 那是误报，直接不报。
		return nil
	}
	sections := self.knownSections()

	var issues []LintIssue
	for _, key := range core.fmt.fileKeys() {
		if _, ok := known[key]; ok {
			continue
		}
		// 该 key 本身未被声明，但它可能是某个已声明项的祖先前缀（例如模型把
		// 整个子树当一个字段读）。凡是已知项的前缀，都不算未知。
		if isPrefixOfAny(key, known) {
			continue
		}
		// ★ 只报「已注册的配置节内部」的未知字段。
		//
		// 整节都没有模型认领时一律跳过：配置模型是**惰性注册**的 —— 只有当代码真的
		// 创建了某个组件，它的配置才进 core。一个只起了 server 的进程，config.json 里
		// 的 client.* / 各业务模块节自然无人认领；vectors 那种运行期动态装模块的系统
		// 更是如此。把这些报成「未知配置项」是纯粹的假阳性，而一个天天误报的检查
		// 等于没有检查 —— 人会直接无视它，真正的拼写错误也跟着被无视。
		//
		// 代价是：历史遗留的整节孤儿（如 client.web.action.* 与 web.action.client.*
		// 并存）本检查不负责发现，那要靠 P3 的迁移器带全量报告来做。
		if !isReportable(key, sections, known) {
			continue
		}
		line, _ := core.fmt.fileLine(key)
		issues = append(issues, LintIssue{
			Path:       key,
			Line:       line,
			Suggestion: nearest(key, known),
		})
	}

	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Line != issues[j].Line {
			return issues[i].Line < issues[j].Line
		}
		return issues[i].Path < issues[j].Path
	})
	return issues
}

// knownKeys 收集所有已注册模型声明过的叶子路径，值是声明它的那个配置节（模型的
// String()）—— isReportable 靠它分辨「模型自己的嵌套字段」与「另一个子组件的节」。
//
// 复用 SaveFromModel 的那套展开（utils.Struct2ItfMap + collectLeaves），确保
// 「模型声明了哪些 key」与「模型会写出哪些 key」用的是同一个答案 —— 两边各写一遍
// 展开逻辑，迟早会对不上，那时 Lint 就开始误报，比不报还糟。
func (self *Config) knownKeys() map[string]string {
	out := make(map[string]string)

	self.Core().models.Range(func(key any, value any) bool {
		model, ok := value.(IConfig)
		if !ok {
			return true
		}
		prefix := strings.ToLower(model.String())
		if prefix == "" {
			return true
		}

		fields := utils.Struct2ItfMap(model)
		for k, v := range fields {
			if strings.EqualFold("config", k) {
				continue // 内嵌基类，与 SaveFromModel 保持一致
			}
			if sub, ok := v.(map[string]any); ok && len(sub) > 0 {
				collectLeaves(sub, prefix+"."+strings.ToLower(k), func(p string, _ any) {
					out[strings.ToLower(p)] = prefix
				})
				continue
			}
			out[prefix+"."+strings.ToLower(k)] = prefix
		}
		return true
	})

	return out
}

// knownSections 返回所有已注册模型的配置节名（即各自的 String()）。
func (self *Config) knownSections() []string {
	var out []string
	self.Core().models.Range(func(key any, value any) bool {
		if model, ok := value.(IConfig); ok {
			if s := strings.ToLower(model.String()); s != "" {
				out = append(out, s)
			}
		}
		return true
	})
	return out
}

// isReportable 报告一个未知 key 是否值得报出来。
//
// 判据是它的**父路径**：只有当父路径确实是「某个已注册模型声明过的结构」时，
// 这个 key 才是该结构里的一个错字；否则它属于一个尚未注册的子节，报了就是假阳性。
//
// 父路径合格的两种情形：
//   - 父路径就是某个已注册配置节本身（server.http.version 的父是 server.http）
//   - 父路径是某个已声明字段路径的前缀（nested.child.bogus 的父是 nested.child，
//     而模型确实声明了 nested.child.name）
//
// 反例是 server.http.client.pool_size：server.http 这一节注册了，但 server.http.client
// 是**子组件**的配置节 —— server 模型自己并没有声明 client.* 下的任何字段（那些实例
// 字段都是 field:"-"）。若只判断「落在已注册节内」，这类 key 会整片被误报；实测对
// kylin 的 config.json 是 77 条里的绝大多数。
//
// 「已声明字段路径的前缀」这一条还要再收一档：父路径必须落在**声明该字段的那个
// 配置节之内**（等于它，或在它下面）。反例来自 kylin 真栈：server 用
// WithConfigPrefixName 把 router 的节从 router.default 搬到 server.http.router，
// 而 router 在 newConfig 时给自己建的 RegistryCacher / Selector 仍留在
// router.default.registry.* / router.default.selector.* —— 于是 router.default
// 是「某个已声明字段的前缀」，它下面那十个早已搬走的字段（recover、upload_buf …）
// 整节被报成未知；server.http.broker 被复用后改挂 server.rpc 时是同一个形状。
// 这些父路径在**子组件的节之上**，不在任何模型自己的结构里，报了就是误报。
func isReportable(key string, sections []string, known map[string]string) bool {
	parent, _ := splitLast(key)
	if parent == "" {
		return false
	}
	for _, s := range sections {
		if parent == s {
			return true
		}
	}
	p := parent + "."
	for k, owner := range known {
		if !strings.HasPrefix(k, p) {
			continue
		}
		if parent == owner || strings.HasPrefix(parent, owner+".") {
			return true
		}
	}
	return false
}

func isPrefixOfAny(key string, known map[string]string) bool {
	p := key + "."
	for k := range known {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// nearest 在已知项里找与 key 最相近的一个。
//
// 只在「同一父路径下」比较叶子名：跨子树的建议（把 server.http.read_timeout 建议成
// client.rpc.read_timeout）看着像模像样，实际会把人引到错误的地方去。
func nearest(key string, known map[string]string) string {
	parent, leaf := splitLast(key)

	best, bestDist := "", 1<<30
	for k := range known {
		kp, kl := splitLast(k)
		if kp != parent {
			continue
		}
		d := editDistance(leaf, kl)
		if d < bestDist {
			best, bestDist = k, d
		}
	}

	// 阈值取叶子名长度的三分之一，至少 1：readtimeout -> read_timeout 距离为 1，
	// 而两个语义无关的字段名通常远超这个距离。
	limit := len(leaf) / 3
	if limit < 1 {
		limit = 1
	}
	if bestDist > limit {
		return ""
	}
	return best
}

func splitLast(path string) (parent, leaf string) {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[:i], path[i+1:]
	}
	return "", path
}

func editDistance(a, b string) int {
	if a == b {
		return 0
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}
