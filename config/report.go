package config

import (
	"fmt"
	"sort"
	"strings"
)

// 配置体检报告。
//
// 与 Lint 的分工：Lint 是**启动时的常规告警**，为了不误报而刻意保守 —— 配置模型
// 是惰性注册的，整节无人认领时它一律跳过。Report 相反，是**按需的一次性体检**，
// 专报那些 Lint 不敢报的东西，并如实标注每条的不确定性由何而来。
//
// 报告只陈述事实，不替人做决定：不说「这是重复，删掉」，只说「这 N 个节结构相同，
// 其中这几个字段的值不一致」。同构节可能是同一个对象的多份配置（registry.Use 按
// name-addr 复用实例，所以确实会出现一实例多配置），也可能就是多个实例
// （client.New / selector.New 每次新建，logger 按名字各一份）—— 这个区分只有
// 看过工厂语义的人能下判断，工具下不了。
//
// 必须在目标进程内运行：纯离线工具不知道业务侧注册了哪些配置模型，只能把整个
// 业务配置报成「无人认领」。设 VOLTS_CONFIG_REPORT=1 可让 service.Start() 自动打印。

// Report 是一份配置体检报告。
type Report struct {
	File      string
	TotalKeys int

	Orphans   []OrphanSection // 无人认领的配置节
	Isomorphs []IsomorphGroup // 结构相同的节组
	Conflicts []ValueConflict // 同构节组内的值分歧
	Secrets   []SecretHit     // 疑似明文密钥
}

// OrphanSection 是文件里存在、却没有任何已注册模型认领的配置节。
type OrphanSection struct {
	Path string
	Line int
	Keys int // 该节下的叶子数
}

// IsomorphGroup 是一组字段集合完全相同的配置节。
type IsomorphGroup struct {
	Fields   []string
	Sections []string
}

// ValueConflict 是同构节组内取值不一致的字段。
type ValueConflict struct {
	Field    string
	Sections []string
	Values   map[string]string // 节路径 -> 该节下此字段的值
}

// SecretHit 是疑似明文写在配置文件里的凭据。
type SecretHit struct {
	Path string
	Line int
}

// 疑似凭据的字段名片段。
var secretHints = []string{"secret", "password", "passwd", "token", "credential", "access_key", "private_key"}

// isLikelySecret 判断一个字段是否是**明文**凭据。
//
// 光看字段名会大量误报：access_token_duration_time、password_length、
// refresh_token_duration_time 都命中了 token / password，但它们是时长和长度。
// 实测对 kylin 的配置误报率 45%（11 条里 5 条），而一份误报过半的安全提示，
// 人扫两眼就会连真的一起略过。
//
// 两条过滤：
//   - 值必须是非空字符串 —— 凭据是字符串，时长和长度是数字。
//   - 值不能是 ${VAR} 环境变量引用 —— 那正是已经外置好的写法，再报就是倒过来
//     催人把它改回明文。
func isLikelySecret(leaf string, raw any) bool {
	str, ok := raw.(string)
	if !ok || str == "" {
		return false
	}
	if hasEnvRef(str) {
		return false
	}
	for _, hint := range secretHints {
		if strings.Contains(leaf, hint) {
			return true
		}
	}
	return false
}

// ReportAll 生成一份配置体检报告。
//
// 名字与 ValidateAll 同理不叫 Report：配置模型都内嵌 Config，方法会被提升上去，
// 一个模型冷不丁多出个返回全局报告的 Report() 方法既没意义又可能撞上别的接口。
func (self *Config) ReportAll() *Report {
	core := self.Core()
	rep := &Report{File: core.fmt.configFile()}

	fileKeys := core.fmt.fileKeys()
	rep.TotalKeys = len(fileKeys)

	sections := self.knownSections()
	known := self.knownKeys()

	// 按父路径把叶子归到节里
	bySection := map[string][]string{}
	values := map[string]string{}
	for _, k := range fileKeys {
		parent, leaf := splitLast(k)
		if parent == "" {
			continue
		}
		raw := core.fmt.rawValue(k)
		bySection[parent] = append(bySection[parent], leaf)
		values[k] = fmt.Sprint(raw)

		if isLikelySecret(leaf, raw) {
			line, _ := core.fmt.fileLine(k)
			rep.Secrets = append(rep.Secrets, SecretHit{Path: k, Line: line})
		}
	}

	// 一、无人认领的节
	for sec, leaves := range bySection {
		if inAnySection(sec, sections) || isLeafOrUnder(sec, known) {
			continue
		}
		line, _ := core.fmt.fileLine(sec + "." + leaves[0])
		rep.Orphans = append(rep.Orphans, OrphanSection{Path: sec, Line: line, Keys: len(leaves)})
	}
	sort.Slice(rep.Orphans, func(i, j int) bool { return rep.Orphans[i].Path < rep.Orphans[j].Path })

	// 二、结构相同的节组
	fingerprint := map[string][]string{}
	for sec, leaves := range bySection {
		if len(leaves) < 2 {
			continue // 单字段节的"同构"没有信息量
		}
		sort.Strings(leaves)
		fingerprint[strings.Join(leaves, "\x00")] = append(fingerprint[strings.Join(leaves, "\x00")], sec)
	}
	for sig, secs := range fingerprint {
		if len(secs) < 2 {
			continue
		}
		sort.Strings(secs)
		fields := strings.Split(sig, "\x00")
		rep.Isomorphs = append(rep.Isomorphs, IsomorphGroup{Fields: fields, Sections: secs})

		// 三、组内值分歧
		for _, f := range fields {
			seen := map[string]bool{}
			vals := map[string]string{}
			for _, s := range secs {
				v := values[s+"."+f]
				vals[s] = v
				seen[v] = true
			}
			if len(seen) > 1 {
				rep.Conflicts = append(rep.Conflicts, ValueConflict{Field: f, Sections: secs, Values: vals})
			}
		}
	}
	sort.Slice(rep.Isomorphs, func(i, j int) bool {
		if len(rep.Isomorphs[i].Sections) != len(rep.Isomorphs[j].Sections) {
			return len(rep.Isomorphs[i].Sections) > len(rep.Isomorphs[j].Sections)
		}
		return rep.Isomorphs[i].Sections[0] < rep.Isomorphs[j].Sections[0]
	})
	sort.Slice(rep.Conflicts, func(i, j int) bool {
		if len(rep.Conflicts[i].Sections) != len(rep.Conflicts[j].Sections) {
			return len(rep.Conflicts[i].Sections) > len(rep.Conflicts[j].Sections)
		}
		return rep.Conflicts[i].Field < rep.Conflicts[j].Field
	})
	sort.Slice(rep.Secrets, func(i, j int) bool { return rep.Secrets[i].Line < rep.Secrets[j].Line })

	return rep
}

// isLeafOrUnder 报告 sec 本身是不是某个已声明的叶子、或落在某个叶子之下 ——
// map 类型的字段在文件里展开成一个「节」，但它是叶子，不是孤儿。
func isLeafOrUnder(sec string, known map[string]string) bool {
	if _, ok := known[sec]; ok {
		return true
	}
	for k := range known {
		if strings.HasPrefix(sec, k+".") {
			return true
		}
	}
	return false
}

func inAnySection(key string, sections []string) bool {
	for _, s := range sections {
		if key == s || strings.HasPrefix(key, s+".") {
			return true
		}
	}
	return false
}

// String 渲染成人类可读的报告。
func (self *Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "配置体检报告  %s  （共 %d 项）\n", self.File, self.TotalKeys)

	fmt.Fprintf(&b, "\n[1] 无人认领的配置节：%d 个\n", len(self.Orphans))
	if len(self.Orphans) > 0 {
		b.WriteString("    这些节在文件里，但本进程没有任何配置模型声明它们。\n")
		b.WriteString("    注意：若对应模块本次未加载（惰性注册），属正常，不必清理。\n")
		// 按顶层命名空间折叠：logger.* 这类天然多实例的家族动辄几十个节，
		// 逐条列出会把真正孤立的节淹掉。
		groups := map[string][]OrphanSection{}
		var order []string
		for _, o := range self.Orphans {
			top := o.Path
			if i := strings.Index(top, "."); i > 0 {
				top = top[:i]
			}
			if _, seen := groups[top]; !seen {
				order = append(order, top)
			}
			groups[top] = append(groups[top], o)
		}
		sort.Strings(order)
		for _, top := range order {
			g := groups[top]
			if len(g) > 3 {
				keys := 0
				for _, o := range g {
					keys += o.Keys
				}
				fmt.Fprintf(&b, "    %-46s %d 个节 / %d 项（首个在第 %d 行）\n",
					top+".*", len(g), keys, g[0].Line)
				continue
			}
			for _, o := range g {
				fmt.Fprintf(&b, "    第 %-5d 行  %-46s （%d 项）\n", o.Line, o.Path, o.Keys)
			}
		}
	}

	fmt.Fprintf(&b, "\n[2] 结构相同的配置节组：%d 组\n", len(self.Isomorphs))
	if len(self.Isomorphs) > 0 {
		b.WriteString("    同构未必是重复：registry / broker 的工厂按 name-addr 复用实例，\n")
		b.WriteString("    同一实例的多份配置才是真重复；而 client / selector / transport / logger\n")
		b.WriteString("    每次新建或按名各一份，多份配置是正确的。请对照工厂语义判断。\n")
		for _, g := range self.Isomorphs {
			fmt.Fprintf(&b, "    %d 个节 × %d 字段：\n", len(g.Sections), len(g.Fields))
			for i, s := range g.Sections {
				if i >= 5 {
					fmt.Fprintf(&b, "        …另 %d 个\n", len(g.Sections)-5)
					break
				}
				fmt.Fprintf(&b, "        %s\n", s)
			}
		}
	}

	fmt.Fprintf(&b, "\n[3] 同构节之间的取值分歧：%d 处  ★ 最需要人工裁决\n", len(self.Conflicts))
	if len(self.Conflicts) > 0 {
		b.WriteString("    结构相同的节，同一字段取值不同。若它们其实指向同一个共享实例，\n")
		b.WriteString("    则哪一份生效取决于谁先创建该实例 —— 这类「改了配置不生效」\n")
		b.WriteString("    改对了地方也可能不生效，是最难排查的一种。\n")
		for _, c := range self.Conflicts {
			fmt.Fprintf(&b, "    字段 %q 在 %d 个节中有分歧：\n", c.Field, len(c.Sections))
			shown := 0
			for _, s := range c.Sections {
				if shown >= 6 {
					fmt.Fprintf(&b, "        …另 %d 个\n", len(c.Sections)-shown)
					break
				}
				fmt.Fprintf(&b, "        %-52s = %s\n", s, c.Values[s])
				shown++
			}
		}
	}

	fmt.Fprintf(&b, "\n[4] 疑似明文凭据：%d 处\n", len(self.Secrets))
	if len(self.Secrets) > 0 {
		b.WriteString("    仅按字段名匹配，只报不改。配置文件通常随代码分发或进入备份。\n")
		b.WriteString("    外置写法：把值改成 ${VAR}，程序装载时从环境变量取；\n")
		b.WriteString("    允许缺省的写 ${VAR:-默认值}，未设置且未声明缺省会报错而非留空。\n")
		b.WriteString("    已写成 ${VAR} 的项不会出现在本清单里。\n")
		for _, s := range self.Secrets {
			fmt.Fprintf(&b, "    第 %-5d 行  %s\n", s.Line, s.Path)
		}
	}

	return b.String()
}

// Inspect 生成默认单例的配置体检报告。
//
// 不叫 Report：那是返回值的类型名，Go 里同包的类型与函数不能重名。
func Inspect() *Report {
	return defaultConfig.ReportAll()
}
