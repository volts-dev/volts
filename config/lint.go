package config

import (
	"reflect"
	"sort"
	"strings"
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

// Lint 报告配置文件里所有「没有任何已绑定结构体声明过」的配置项。
//
// 必须在全部组件装配完毕之后调用 —— 差集的正确性依赖于「所有结构体都已绑定」。
// service.Start() 会自动调一次；独立使用时自行掌握时机。
func (self *Config) Lint() []LintIssue {
	core := self.Core()

	known := self.knownKeys()
	if len(known) == 0 {
		// 一个结构体都没绑定，此时任何 key 都会被判为未知 —— 那是误报，直接不报。
		return nil
	}
	sections := self.knownSections()

	var issues []LintIssue
	for _, key := range core.fmt.fileKeys() {
		if _, ok := known[key]; ok {
			continue
		}
		// ★ 只报「某个已绑定结构体内部」的未知字段：key 的父路径必须正是一个结构体节
		//   （根节，或根节里某个子结构体字段）。
		//
		// 父路径不是结构体节的一律跳过：配置是**惰性绑定**的 —— 只有当代码真的创建了
		// 某个组件，它的配置才进树。一个只起了 server 的进程，config.json 里的 client.*
		// / 各业务模块节自然无人认领；vectors 那种运行期动态装模块的系统更是如此。把
		// 这些报成「未知配置项」是纯粹的假阳性，而一个天天误报的检查等于没有检查 ——
		// 人会直接无视它，真正的拼写错误也跟着被无视。整节孤儿交给 Report。
		//
		// 「父路径是结构体节」这一条判据是结构体图直接给出的，不用再从字符串反推谁
		// 声明了谁：一个 map 字段下的键（server.http.metadata.foo）父路径是叶子不是节，
		// 不报；一个子组件被搬走后留下的旧节，父路径已不在树上，也不报。
		if !isReportable(key, sections) {
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

// knownKeys 收集所有已绑定结构体声明过的叶子路径，值是叶子所在的那个结构体节路径。
//
// 走的是与灌默认值 / 装载完全相同的走查器（walk.go），所以「结构体声明了哪些 key」
// 与「结构体会写出 / 读回哪些 key」永远是同一个答案。
func (self *Config) knownKeys() map[string]string {
	out := make(map[string]string)
	self.Core().nodes.Range(func(_ any, value any) bool {
		n := value.(*node)
		rv, ok := structValueOf(n.ptr)
		if !ok {
			return true
		}
		walk(rv, n.path, nil, func(p string, _ reflect.Value) {
			parent, _ := splitLast(p)
			out[p] = parent
		})
		return true
	})
	return out
}

// knownSections 返回树上所有结构体节的路径：每个根节，加上根节里每个子结构体字段。
func (self *Config) knownSections() []string {
	seen := map[string]bool{}
	self.Core().nodes.Range(func(_ any, value any) bool {
		n := value.(*node)
		rv, ok := structValueOf(n.ptr)
		if !ok {
			return true
		}
		walk(rv, n.path, func(p string, _ reflect.Value) { seen[p] = true }, nil)
		return true
	})
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// isReportable 报告一个未知 key 是否值得报出来：它的父路径必须是一个结构体节。
func isReportable(key string, sections []string) bool {
	parent, _ := splitLast(key)
	if parent == "" {
		return false
	}
	for _, s := range sections {
		if parent == s {
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
