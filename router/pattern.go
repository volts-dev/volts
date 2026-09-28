package router

import (
	"fmt"
	"regexp"
	"strings"
)

/*
路径模式的**独立**匹配器：不经过路由树，把一条模式与一段路径比对并取出参数。

# 它是给谁用的

上层（vectors 的站点应用挂载）要把一整族前台路由的对外前缀交给数据决定：用户把网店从
`/shop` 改名成 `/store`，`/store/cart`、`/store/page/2` 整棵子树跟着走。

前缀属于**某个租户的某个站点**，而路由树是装配期建的、一个进程服务所有租户，所以
`/store` 这类前缀**不能**注册进树。改名后的请求只能落到根级通配 `/*` 上，由上层拿余段
（`cart`、`page/2`）自己去比对那一族路由声明的**相对模式**（`page/{page}`）。

树的 Match 在这里用不上：它要的是一棵注册好的树 + 一个方法名，而这里要比的是**一条**
已知模式。所以有了这个匹配器。

# 与路由树的关系：能一致的必须一致，不能一致的要写下来

同一条相对路由会被注册**两次**——按出厂前缀进路由树（`/shop/page/{page}`，绝对路由
原地可用），同时作为相对模式交给这里（改名后的 `/store/page/2` 走这条）。两处对
`{page}` 的理解一旦分叉，症状是"同一个地址改名前能打开、改名后 404"，零报错零日志。
`pattern_test.go` 的 `TestPatternParityWithRouteTree` 就是拿真树逐条对着判的。

支持的形态（与树同判，有用例逐条钉着）：

	page/{page}                一段一个具名参数
	page/{page:int}            数字段（同树的 NumberType）
	tag/{name:string}          字母段（同树的 CharType）
	{mode:[read|write]}        枚举段
	{code:[A-Z]{3}}            正则段（整段锚定）
	image/{id}-{unique}/x      一段里字面量与参数混排（树支持，实测 2026-09-29）
	download/*rest             通配，吃掉剩下全部（含 `/`），必须是最后一段
	""                         空模式，只匹配空余段（= 挂载点本身，如 `/store`）

**三处有意比树严**，都只在畸形 URL 上成立，方向都是"这里拒、树放过"（反方向才危险：
那会让改名后的地址比改名前多出可达路径）。三条各有用例，树的现状也在用例里测着，树哪天
变了就会红：

 1. **尾随斜杠**。`cart/` 与 `cart` 是两段不同的路径，这里判不匹配。树侧是 TRouter 在
    匹配失败后用 301 归一（trailing_slash_test.go），判据是"原路径匹配不到、另一种写法
    匹配得到"。挂载派发照这条规则再跳一次即可——**不要**把匹配器放宽：宽了就没人能回答
    "哪个地址是规范地址"，canonical 和 SEO 都挂在这个答案上。
 2. **末段参数不吞 `/`**。树里一个末尾的 `{page}` 若没有子节点，会把 `2/3` 整个吞下
    （`page="2/3"`，类型是 `int` 时才挡得住）。这里一个参数只吃一段。
 3. **参数值不许为空**。树能把 `image/-abc/x` 匹成 `id=""`。这里拒。

`{a}{b}` 这种**相邻两个参数中间没有字面量**的写法，树是静默不匹配（validNodes 那一层
把它拦在注册时，但不报错），这里在装配期直接报错。

# 请求路径上用 CompilePattern，不要用 MatchPattern

`MatchPattern` 每次都重新编译（带正则的还要 regexp.Compile）。装配期编译一次、
请求期只 Match 是本仓的通例（中间件、路由、OpenAPI 全是这个形状）。
*/

// Pattern 是一条编译好的路径模式。装配期用 CompilePattern 得到，请求期只调 Match。
type Pattern struct {
	raw  string
	segs []patternSeg
	// anyAt 是通配段的下标（只可能是最后一段），-1 表示没有通配段。
	anyAt int
}

// patternSeg 是模式里的一段（两个 `/` 之间）。普通段由字面量与参数交替组成
// （`{id}-{unique}` 是三部分：参数、字面量 `-`、参数）；通配段整段一个参数。
type patternSeg struct {
	any   bool   // 通配段（`*name`）：吃掉剩下全部，含 `/`
	name  string // 通配段的参数名（`*` 的名字是空串，与树一致）
	parts []segPart
}

type segPart struct {
	isParam bool
	text    string // 字面量原文，或参数名
	typ     ContentType
	values  []string // 枚举段的取值
	re      *regexp.Regexp
}

// String 返回模式原文（本仓约定：每个对象都实现 String）。
func (self *Pattern) String() string {
	return self.raw
}

// ParamNames 返回模式里声明的参数名，按出现顺序。
func (self *Pattern) ParamNames() []string {
	var out []string
	for i := range self.segs {
		if self.segs[i].any {
			out = append(out, self.segs[i].name)
			continue
		}
		for _, p := range self.segs[i].parts {
			if p.isParam {
				out = append(out, p.text)
			}
		}
	}
	return out
}

// CompilePattern 在**装配期**编译一条相对路径模式。
//
// 模式不带前导 `/`（带了也接受，等价）。返回的错误是给写声明的人看的：它要么说"这个形态
// 树也不支持"，要么说"这个形态两边理解不一致"——两种都必须在进程起来之前被看到，
// 而不是变成运行期的 404。
func CompilePattern(pattern string) (*Pattern, error) {
	raw := pattern
	p := &Pattern{raw: raw, anyAt: -1}

	pattern = strings.TrimPrefix(pattern, "/")
	if pattern == "" {
		// 空模式：挂载点本身。合法，且只匹配空余段。
		return p, nil
	}
	if strings.HasSuffix(pattern, "/") {
		return nil, fmt.Errorf("模式 %q 以 `/` 结尾：尾随斜杠不是一段路径，"+
			"归一是派发处 301 的活（见 pattern.go 文件头），模式本身不许带", raw)
	}

	segs := strings.Split(pattern, "/")
	for i, s := range segs {
		if s == "" {
			return nil, fmt.Errorf("模式 %q 里有空段（连续的 `//`）", raw)
		}

		if strings.HasPrefix(s, "*") {
			if i != len(segs)-1 {
				return nil, fmt.Errorf("模式 %q 的通配段 %q 不是最后一段："+
					"通配吃掉剩下全部（含 `/`），它后面的段永远匹配不到", raw, s)
			}
			name := s[1:]
			if err := validParamName(name, raw, s); err != nil {
				return nil, err
			}
			p.segs = append(p.segs, patternSeg{any: true, name: name})
			p.anyAt = i
			continue
		}
		if strings.Contains(s, "*") {
			return nil, fmt.Errorf("模式 %q 的段 %q 把 `*` 写在了段中间："+
				"通配段必须整段以 `*` 开头", raw, s)
		}

		seg, err := compileSegment(s, raw)
		if err != nil {
			return nil, err
		}
		p.segs = append(p.segs, *seg)
	}

	return p, nil
}

// compileSegment 把一段切成「字面量 / 参数」交替的若干部分。
func compileSegment(s, raw string) (*patternSeg, error) {
	seg := &patternSeg{}
	var lit strings.Builder

	flushLit := func() {
		if lit.Len() > 0 {
			seg.parts = append(seg.parts, segPart{text: lit.String()})
			lit.Reset()
		}
	}

	for i := 0; i < len(s); {
		switch s[i] {
		case '{':
			end, err := matchBrace(s, i)
			if err != nil {
				return nil, fmt.Errorf("模式 %q 的段 %q: %v", raw, s, err)
			}
			flushLit()
			if n := len(seg.parts); n > 0 && seg.parts[n-1].isParam {
				return nil, fmt.Errorf("模式 %q 的段 %q 里两个参数紧挨着、中间没有字面量："+
					"路由树对这种写法是**静默不匹配**的（注册得上、永远匹配不到），"+
					"中间加一个分隔字面量，或者合成一个参数再在 handler 里切", raw, s)
			}
			part, err := compileParam(s[i+1:end], raw, s)
			if err != nil {
				return nil, err
			}
			seg.parts = append(seg.parts, *part)
			i = end + 1
		case '}':
			return nil, fmt.Errorf("模式 %q 的段 %q 里有没配对的 `}`", raw, s)
		default:
			lit.WriteByte(s[i])
			i++
		}
	}
	flushLit()
	return seg, nil
}

// matchBrace 返回与 s[open]（必须是 `{`）配对的 `}` 的下标。嵌套要算（正则里的
// `{3}` 是合法内容，树的 parsePath 也是按层数找的）。
func matchBrace(s string, open int) (int, error) {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i, nil
			}
		}
	}
	return -1, fmt.Errorf("`{` 没有配对的 `}`")
}

// compileParam 解析 `{}` 里的内容：`name`、`name:int`、`name:string`、
// `name:[枚举|枚举]`、`name:[正则]`。
func compileParam(content, raw, s string) (*segPart, error) {
	if content == "" {
		return nil, fmt.Errorf("模式 %q 的段 %q 里是空参数 `{}`", raw, s)
	}

	name, spec := content, ""
	if idx := strings.Index(content, ":"); idx != -1 {
		name, spec = content[:idx], content[idx+1:]
	} else if strings.ContainsAny(name, "()[]|*+?.\\^$") {
		// 树里 `{[0-9]+}` 这种老写法会把参数名定成 `var`，谁都不会去读那个名字。
		return nil, fmt.Errorf("模式 %q 的段 %q 用了「只有正则、没有参数名」的老写法："+
			"树会把名字定成 `var`，两边对不上，这里要求**显式具名**，写成 {name:[正则]}", raw, s)
	}
	if name == "" {
		return nil, fmt.Errorf("模式 %q 的段 %q 没给参数名", raw, s)
	}
	if err := validParamName(name, raw, s); err != nil {
		return nil, err
	}

	part := &segPart{isParam: true, text: name, typ: AllType}
	switch {
	case spec == "":
	case spec == "int":
		part.typ = NumberType
	case spec == "string":
		part.typ = CharType
	case strings.HasPrefix(spec, "["):
		// 与树的 parsePath 同规：`[` 开头就是"模式"。**只有**同时以 `]` 结尾才剥掉
		// 那对方括号（`[read|write]` → 枚举）；`[A-Z]{3}` 这种带量词的整串当正则，
		// 剥了就变成另一个表达式。
		body := spec
		if strings.HasSuffix(spec, "]") {
			body = spec[1 : len(spec)-1]
		}
		switch {
		case body == "":
			return nil, fmt.Errorf("模式 %q 的段 %q 的 `[]` 里是空的", raw, s)
		case body == `\d+` || body == "0-9]+":
			part.typ = NumberType
		case body == "a-z]+" || body == "A-Za-z]+" || body == "a-zA-Z]+":
			part.typ = CharType
		case !strings.ContainsAny(body, "()[]*+?.^$\\"):
			// 没有正则元字符：当枚举。单值就是只有一个取值的枚举。
			part.values = strings.Split(body, "|")
		default:
			re, err := regexp.Compile("^(" + body + ")$")
			if err != nil {
				return nil, fmt.Errorf("模式 %q 的段 %q 的正则编译失败: %v", raw, s, err)
			}
			part.re = re
		}
	default:
		return nil, fmt.Errorf("模式 %q 的段 %q 的类型 %q 不认识："+
			"可用的是 int、string、[枚举|枚举]、[正则]", raw, s, spec)
	}
	return part, nil
}

// validParamName 只放行 [A-Za-z0-9_]。树对 `*name` 就是按 IsAlnumByte 截的，
// 别的字符在那边会被静默截成另一个名字。
func validParamName(name, raw, seg string) error {
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '_' ||
			(c >= '0' && c <= '9') ||
			(c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') {
			continue
		}
		return fmt.Errorf("模式 %q 的段 %q 里参数名 %q 含非法字符 %q："+
			"只允许字母、数字、下划线（树按这个集合截名字，别的字符会被静默截掉）",
			raw, seg, name, string(c))
	}
	return nil
}

// Match 把 path 与本模式比对。命中时返回按声明顺序排列的参数（没有参数则为空切片）。
//
// path 不带前导 `/`（带了也接受，等价）。**大小写敏感**，与路由树的静态段一致。
func (self *Pattern) Match(path string) (Params, bool) {
	path = strings.TrimPrefix(path, "/")

	if len(self.segs) == 0 {
		return Params{}, path == ""
	}

	if path == "" {
		// 只有"通配就是第一段"能吃下空余段（树的 AnyNode 对空串有合法语义）。
		if self.anyAt == 0 {
			return Params{{Name: self.segs[0].name, Value: ""}}, true
		}
		return nil, false
	}

	pathSegs := strings.Split(path, "/")
	fixed := len(self.segs)
	if self.anyAt >= 0 {
		fixed = self.anyAt // 通配之前的固定段数
		if len(pathSegs) < fixed {
			return nil, false
		}
	} else if len(pathSegs) != fixed {
		return nil, false
	}

	params := make(Params, 0, len(self.segs))
	for i := 0; i < fixed; i++ {
		var ok bool
		params, ok = matchSegment(&self.segs[i], pathSegs[i], params)
		if !ok {
			return nil, false
		}
	}

	if self.anyAt >= 0 {
		rest := strings.Join(pathSegs[fixed:], "/")
		params = append(params, param{Name: self.segs[self.anyAt].name, Value: rest})
	}
	return params, true
}

// matchSegment 匹配一个普通段，命中时把参数追加进 params。
func matchSegment(seg *patternSeg, got string, params Params) (Params, bool) {
	rest := got
	for i := range seg.parts {
		part := &seg.parts[i]
		if !part.isParam {
			if !strings.HasPrefix(rest, part.text) {
				return nil, false
			}
			rest = rest[len(part.text):]
			continue
		}

		var val string
		if i == len(seg.parts)-1 {
			val, rest = rest, ""
		} else {
			// 编译期保证了参数后面紧跟的一定是字面量。取**第一次**出现的位置，
			// 与树的 strings.Index 同向。
			lit := seg.parts[i+1].text
			idx := strings.Index(rest, lit)
			if idx <= 0 { // idx==0 即参数为空：树能匹（id=""），这里拒，见文件头第 3 条
				return nil, false
			}
			val, rest = rest[:idx], rest[idx:]
		}
		if !matchParamValue(part, val) {
			return nil, false
		}
		params = append(params, param{Name: part.text, Value: val})
	}
	if rest != "" {
		return nil, false
	}
	return params, true
}

func matchParamValue(part *segPart, got string) bool {
	if got == "" {
		return false // 参数段不许为空，见文件头第 3 条
	}
	if part.re != nil {
		return part.re.MatchString(got)
	}
	if !validType(got, part.typ) {
		return false
	}
	if len(part.values) > 0 {
		for _, v := range part.values {
			if got == v {
				return true
			}
		}
		return false
	}
	return true
}

// MatchPattern 是 CompilePattern + Match 的一次性写法：**每次都重新编译**。
//
// 只给测试和一次性调用。请求路径上一律装配期 CompilePattern、请求期 Match ——
// 本仓的规矩是"在注册处分支，不在请求路径上分支"。
//
// 模式不合法时返回 false。**不合法要靠 CompilePattern 在装配期拦**（siteapp.Register
// 就是这么做的）：到了请求期，不合法与不匹配都只是一个 404，分不出来。
func MatchPattern(pattern, path string) (Params, bool) {
	p, err := CompilePattern(pattern)
	if err != nil {
		return nil, false
	}
	return p.Match(path)
}

// SetPathParams 把一组路径参数注入本上下文，供**挂载派发**用。
//
// 路径参数平时只由路由树填（`ctx.PathParams()` 读的就是它）。改名后的 `/store/page/2`
// 命中的是根级通配 `/*`，树只会给出一个"余段"参数；真正的 `{page}=2` 是上层拿相对模式
// 比出来的，要塞回上下文里，否则 handler 里 `ctx.PathParams().FieldByName("page")`
// 读到的是空值——而那**不报错**，只是分页永远停在第一页。
//
// **追加**语义：树已经填进去的参数留着，同名的被覆盖。所以它不走 setPathParams ——
// 那一支是 Router 在派发前用的，`len(p) > 0` 时会整个换掉参数集。
func (self *THttpContext) SetPathParams(p Params) {
	if len(p) == 0 {
		return
	}
	ps := self.PathParams() // 永不返回 nil
	for _, prm := range p {
		ps.SetByField(prm.Name, prm.Value)
	}
}
