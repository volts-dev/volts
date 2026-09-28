package router

import (
	"net/http"
	"sort"
	"strings"
	"testing"
)

/*
路径模式匹配器的用例。五组，各自守一件事：

 1. TestPatternMatch —— 语义本身（参数段、混排段、通配、尾随斜杠、大小写、空余段）。
 2. TestPatternParityWithRouteTree —— ★ **与路由树逐条同判**。同一条相对路由会被
    注册两次（出厂前缀进树、相对模式进匹配器），两边分叉的症状是"同一地址改名前打得开、
    改名后 404"，没有报错、没有日志。这一组是这个文件存在的主要理由。
 3. TestPatternIsStricterThanTreeOnPurpose —— 三处**有意**比树严的地方，连树的现状
    一起测：树哪天变了这里就红，而不是悄悄分叉。
 4. TestPatternCompileErrors —— 非法形态都在装配期拦住，且错误里说得出为什么。
 5. TestSetPathParams… / TestRootCatchallAcceptsPost —— 派发侧要用到的两件能力。
*/

func TestPatternMatch(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		ok      bool
		params  map[string]string
	}{
		// 挂载点本身：空模式只吃空余段
		{"", "", true, map[string]string{}},
		{"", "cart", false, nil},

		// 静态
		{"cart", "cart", true, map[string]string{}},
		{"cart/update", "cart/update", true, map[string]string{}},
		{"cart", "Cart", false, nil},                 // 大小写敏感，与树的静态段一致
		{"cart", "cart/update", false, nil},          // 多余段
		{"cart/update", "cart", false, nil},          // 缺段
		{"cart", "/cart", true, map[string]string{}}, // 前导 / 等价

		// 尾随斜杠：不同路径。归一是派发处 301 的活（见 pattern.go 文件头）
		{"cart", "cart/", false, nil},
		{"page/{page}", "page/2/", false, nil},

		// 具名参数
		{"page/{page}", "page/2", true, map[string]string{"page": "2"}},
		{"page/{page}", "page/", false, nil},    // 空参数段
		{"page/{page}", "page/2/3", false, nil}, // 一个参数只吃一段（树会吞掉 `/`，见下一组）
		{"{slug}", "office-chair-2082908153950769152", true,
			map[string]string{"slug": "office-chair-2082908153950769152"}},
		{"category/{cat}/image", "category/2082908144756854784/image", true,
			map[string]string{"cat": "2082908144756854784"}},
		{"{a}/x/{b}", "1/x/2", true, map[string]string{"a": "1", "b": "2"}},

		// 一段里字面量与参数混排（树支持，所以这里也支持）
		{"image/{id}-{unique}/x", "image/7-abc/x", true,
			map[string]string{"id": "7", "unique": "abc"}},
		{"image/{id}-{unique}/x", "image/7-a-b/x", true,
			map[string]string{"id": "7", "unique": "a-b"}}, // 取第一个 `-`，与树同向
		{"image/{id}-{unique}/x", "image/7/x", false, nil},
		{"image/{id}-{unique}/x", "image/-abc/x", false, nil}, // 参数不许为空（树能匹成 id=""）
		{"x{y}", "xab", true, map[string]string{"y": "ab"}},
		{"{id}.png", "7.png", true, map[string]string{"id": "7"}},

		// 类型
		{"page/{page:int}", "page/2", true, map[string]string{"page": "2"}},
		{"page/{page:int}", "page/two", false, nil},
		{"tag/{name:string}", "tag/desk", true, map[string]string{"name": "desk"}},
		{"tag/{name:string}", "tag/desk2", false, nil},

		// 枚举与正则
		{"{mode:[read|write]}", "read", true, map[string]string{"mode": "read"}},
		{"{mode:[read|write]}", "delete", false, nil},
		{"iso/{code:[A-Z]{3}}", "iso/CNY", true, map[string]string{"code": "CNY"}},
		{"iso/{code:[A-Z]{3}}", "iso/cny", false, nil},
		{"iso/{code:[A-Z]{3}}", "iso/CNYX", false, nil},

		// 通配：吃掉剩下全部，含 `/`；空余段也合法（树的 AnyNode 语义）
		{"download/*rest", "download/a/b/c.pdf", true, map[string]string{"rest": "a/b/c.pdf"}},
		{"download/*rest", "download/", true, map[string]string{"rest": ""}},
		{"download/*rest", "download", true, map[string]string{"rest": ""}},
		{"download/*rest", "other/a", false, nil},
		{"*rest", "a/b", true, map[string]string{"rest": "a/b"}},
		{"*rest", "", true, map[string]string{"rest": ""}},
		{"*", "a/b", true, map[string]string{"": "a/b"}}, // 无名通配，与树一致

		// 空段（连续 //）不匹配
		{"cart/update", "cart//update", false, nil},
	}

	for _, c := range cases {
		t.Run(c.pattern+" ~ "+c.path, func(t *testing.T) {
			got, ok := MatchPattern(c.pattern, c.path)
			if ok != c.ok {
				t.Fatalf("匹配 = %v，期望 %v（参数 %v）", ok, c.ok, describeParams(got))
			}
			if !ok {
				return
			}
			if diff := paramsDiff(got, c.params); diff != "" {
				t.Errorf("参数不对：%s", diff)
			}
		})
	}
}

// ★ 与路由树逐条同判。
//
// 做法：同一条相对模式，一边挂到 `/probe/<pattern>` 注册进真树、用 `/probe/<path>` 去
// Match；一边直接 `Pattern.Match(path)`。命中与否、参数键值都必须一致。
//
// 为什么值得这么绕：这一族路由**本来就注册两次**（出厂前缀 `/shop/...` 进树，相对模式
// 交给匹配器给改名后的 `/store/...` 用）。两边对 `{page}` 的理解差一点，症状是
// "`/shop/page/2` 好的，`/store/page/2` 404"——零报错，而且只有配过改名的租户看得见。
func TestPatternParityWithRouteTree(t *testing.T) {
	cases := []struct{ pattern, path string }{
		{"cart", "cart"},
		{"cart", "Cart"},
		{"cart/update", "cart/update"},
		{"cart/update", "cart"},
		{"page/{page}", "page/2"},
		{"page/{page:int}", "page/2"},
		{"page/{page:int}", "page/two"},
		{"page/{page:int}", "page/2/3"},
		{"tag/{name:string}", "tag/desk"},
		{"tag/{name:string}", "tag/desk2"},
		{"{mode:[read|write]}", "read"},
		{"{mode:[read|write]}", "delete"},
		{"category/{cat}/image", "category/1/image"},
		{"category/{cat}/image", "category/1/2/image"},
		{"{a}/x/{b}", "1/x/2"},
		{"image/{id}-{unique}/x", "image/7-abc/x"},
		{"image/{id}-{unique}/x", "image/7-a-b/x"},
		{"image/{id}-{unique}/x", "image/7/x"},
		{"x{y}", "xab"},
		{"iso/{code:[A-Z]{3}}", "iso/CNY"},
		{"iso/{code:[A-Z]{3}}", "iso/cny"},
		{"id/{n:[0-9]+}", "id/42"},
		{"id/{n:[0-9]+}", "id/4x"},
		{"download/*rest", "download/a/b"},
	}

	for _, c := range cases {
		t.Run(c.pattern+" ~ "+c.path, func(t *testing.T) {
			p, err := CompilePattern(c.pattern)
			if err != nil {
				t.Fatalf("CompilePattern(%q): %v", c.pattern, err)
			}
			gotParams, gotOK := p.Match(c.path)
			treeParams, treeOK := treeMatch(t, c.pattern, c.path)

			if gotOK != treeOK {
				t.Fatalf("模式 %q 对 %q：匹配器判 %v，路由树判 %v —— 两套语义分叉了。\n"+
					"这类分叉的症状是「改名前打得开、改名后 404」，零报错。\n"+
					"确实要分叉的话，写进 TestPatternIsStricterThanTreeOnPurpose 并说明理由。",
					c.pattern, c.path, gotOK, treeOK)
			}
			if !gotOK {
				return
			}
			// 树的参数顺序是回溯着追加的，只比键值集合。
			if diff := paramsDiff(gotParams, paramsMap(treeParams)); diff != "" {
				t.Errorf("模式 %q 对 %q 的参数与路由树不一致：%s", c.pattern, c.path, diff)
			}
		})
	}
}

// 三处有意比树严的地方。**树的现状一起测**：这些不是"匹配器偷懒"，是明知树更宽松而
// 选了严的那一边；树哪天变了（或者有人把这里放宽了），这一组会红。
//
// 方向都是「这里拒、树放过」。反方向才危险：那会让改名后的地址比改名前**多出**可达路径。
func TestPatternIsStricterThanTreeOnPurpose(t *testing.T) {
	cases := []struct {
		name, pattern, path, why string
	}{
		{
			name:    "尾随斜杠",
			pattern: "page/{page}", path: "page/2/",
			why: "归一是派发处 301 的活；宽松了就回答不了「哪个是规范地址」，canonical/SEO 全挂在这上面",
		},
		{
			name:    "末段参数不吞 `/`",
			pattern: "page/{page}", path: "page/2/3",
			why: "树里末尾的 {page} 没有子节点时会把 `2/3` 整个吞下（类型是 int 才挡得住）",
		},
		{
			name:    "参数值不许为空",
			pattern: "image/{id}-{unique}/x", path: "image/-abc/x",
			why: "树能把它匹成 id=\"\"，handler 拿到空 id 再去查库",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, ok := MatchPattern(c.pattern, c.path); ok {
				t.Errorf("匹配器不该匹 %q ~ %q（理由：%s）", c.pattern, c.path, c.why)
			}
			if _, treeOK := treeMatch(t, c.pattern, c.path); !treeOK {
				t.Errorf("路由树现在也不匹 %q ~ %q 了 —— 那这条「有意更严」已经不是分叉，"+
					"把它挪回 TestPatternParityWithRouteTree", c.pattern, c.path)
			}
		})
	}
}

func TestPatternCompileErrors(t *testing.T) {
	cases := []struct {
		name, pattern, wantIn string
	}{
		{"尾随斜杠", "cart/", "尾随斜杠"},
		{"空段", "cart//update", "空段"},
		{"通配不在最后", "*rest/x", "不是最后一段"},
		{"通配写在段中间", "a*b", "整段以 `*` 开头"},
		{"相邻两个参数", "{a}{b}", "静默不匹配"},
		{"空参数", "{}", "空参数"},
		{"没配对的花括号", "{id", "没有配对"},
		{"多出来的右花括号", "id}", "没配对的 `}`"},
		{"老式无名正则", "{[0-9]+}", "显式具名"},
		{"参数名非法", "{a-b}", "非法字符"},
		{"类型不认识", "{id:uuid}", "不认识"},
		{"正则编译失败", "{id:[(]}", "正则编译失败"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := CompilePattern(c.pattern)
			if err == nil {
				t.Fatalf("CompilePattern(%q) 应当报错", c.pattern)
			}
			if !strings.Contains(err.Error(), c.wantIn) {
				t.Errorf("错误里应当出现 %q，得到：%v", c.wantIn, err)
			}
		})
	}
}

// SetPathParams 要能把匹配器算出来的参数塞进上下文，且**不擦掉**树已经填好的。
//
// 漏了这一步不报错：handler 里 `ctx.PathParams().FieldByName("page")` 读到空值，
// 分页永远停在第一页。
func TestSetPathParamsInjectsIntoContext(t *testing.T) {
	r := New()
	t.Cleanup(r.Close)

	var gotPage, gotSlug, gotAny string
	grp := NewGroup()
	grp.Url([]string{"GET"}, "/mount/{slug}/*rest", func(ctx *THttpContext) {
		// 模拟挂载派发：拿相对模式比出来的参数注入
		params, ok := MatchPattern("page/{page}", ctx.PathParams().FieldByName("rest").AsString())
		if !ok {
			ctx.Respond([]byte("rel not matched"))
			return
		}
		ctx.SetPathParams(params)
		gotPage = ctx.PathParams().FieldByName("page").AsString()
		gotSlug = ctx.PathParams().FieldByName("slug").AsString()
		gotAny = ctx.PathParams().FieldByName("rest").AsString()
		ctx.Respond([]byte("ok"))
	})
	r.RegisterGroup(grp)

	w := do(t, r, "GET", "/mount/store/page/2")
	if w.Code != http.StatusOK || w.Body.String() != "ok" {
		t.Fatalf("状态 %d body=%q —— 这条用例没测到东西", w.Code, w.Body.String())
	}
	if gotPage != "2" {
		t.Errorf("注入的 page = %q，期望 \"2\"", gotPage)
	}
	if gotSlug != "store" {
		t.Errorf("树填的 slug 被擦掉了（得到 %q）：SetPathParams 是追加语义", gotSlug)
	}
	if gotAny != "page/2" {
		t.Errorf("树填的 rest = %q，期望 \"page/2\"", gotAny)
	}
}

// 根级通配必须能收 POST。
//
// 改名之后 `/store/cart/update` 这类 POST 端点在树上不存在（树里只有 `/shop/cart/update`），
// 它们全落到根级通配上。今天 website 的 `/*` 只注册了 GET，所以这一条是**能力**确认：
// 树与派发这一路对 POST 没有额外限制，P3 只要把方法列表加上。
//
// ★ 能力确认不等于可以随手放开：根级通配一旦收 POST，"任意 POST 都能进通配"就成立了。
// 派发处必须先确认目标命中了某个 app 的相对路由，不命中一律 404（设计文档 §2.3）。
func TestRootCatchallAcceptsPost(t *testing.T) {
	r := New()
	t.Cleanup(r.Close)

	var gotMethod, gotRest string
	grp := NewGroup()
	grp.Url([]string{"GET", "POST"}, "/*", func(ctx *THttpContext) {
		gotMethod = ctx.Request().Method
		gotRest = ctx.PathParams().FieldByName("").AsString()
		ctx.Respond([]byte("catchall"))
	})
	r.RegisterGroup(grp)

	w := do(t, r, "POST", "/store/cart/update")
	if w.Code != http.StatusOK {
		t.Fatalf("POST 打到根级通配得到 %d（body=%q）—— 改名后的 POST 端点全在这条路上",
			w.Code, w.Body.String())
	}
	if gotMethod != "POST" {
		t.Errorf("handler 收到的方法 = %q", gotMethod)
	}
	if gotRest != "store/cart/update" {
		t.Errorf("通配捕获的余段 = %q，期望 \"store/cart/update\"", gotRest)
	}

	// 没注册的方法不许被通配吃掉。
	gotMethod = ""
	w = do(t, r, "PUT", "/store/cart/update")
	if w.Code == http.StatusOK || gotMethod == "PUT" {
		t.Errorf("PUT 也进了通配（状态 %d，handler 收到 %q）：通配只该服务声明过的方法",
			w.Code, gotMethod)
	}
}

// ── 小工具 ──────────────────────────────────────────────────────────────────

// treeMatch 把一条相对模式挂到真路由树的 /probe 下，用同样的相对路径去匹配。
func treeMatch(t *testing.T, pattern, path string) (Params, bool) {
	t.Helper()
	grp := NewGroup()
	grp.Url([]string{"GET"}, "/probe/"+pattern, func(ctx *THttpContext) {})
	rt, params := grp.GetRoutes().Match("GET", "/probe/"+path)
	return params, rt != nil
}

func paramsMap(p Params) map[string]string {
	out := map[string]string{}
	for _, v := range p {
		out[v.Name] = v.Value
	}
	return out
}

func paramsDiff(got Params, want map[string]string) string {
	gotM := paramsMap(got)
	if len(gotM) != len(got) {
		return "参数里有重名键：" + describeParams(got)
	}
	var problems []string
	for k, v := range want {
		if g, ok := gotM[k]; !ok {
			problems = append(problems, "缺 "+k)
		} else if g != v {
			problems = append(problems, k+" = "+g+"，期望 "+v)
		}
	}
	for k := range gotM {
		if _, ok := want[k]; !ok {
			problems = append(problems, "多出 "+k+" = "+gotM[k])
		}
	}
	sort.Strings(problems)
	return strings.Join(problems, "; ")
}

func describeParams(p Params) string {
	parts := make([]string, 0, len(p))
	for _, v := range p {
		parts = append(parts, v.Name+"="+v.Value)
	}
	return strings.Join(parts, ",")
}
