package router

import "testing"

// 反代必须把 PathRewriter 摘掉的前缀还给上游。
//
// PathRewriter 在**路由匹配之前**改写 r.URL.Path（/fr/shop → /shop），而反代的 SetURL 拼的
// 正是这个改写后的路径 —— 网关转发出去的是 /shop，上游再也无从得知本次请求带过语言。
// 真机实测:经网关请求 /fr 拿回的是英文页，上游还回了一个"已按浏览器语言评估过"的 cookie，
// 因为在它看来这就是一次无前缀请求。
//
// 这个 bug **随语言而异**，所以格外难认:网关认得的 url_code 才会被摘掉（进而丢失），
// 认不得的原样转发、上游自己剥得好好的。于是 /zh 正常而 /fr 掉语言，看起来像"某一门语言
// 坏了"，而不是"网关吃掉了前缀"。
func TestProxyPathWithPrefix(t *testing.T) {
	for _, c := range []struct {
		name   string
		out    string // SetURL 拼好的路径
		base   string // 上游服务地址里的路径部分
		prefix string
		want   string
	}{
		{"根部上游", "/shop", "", "/fr", "/fr/shop"},
		{"上游路径带斜杠", "/shop", "/", "/fr", "/fr/shop"},
		{"首页", "/", "", "/zh", "/zh/"},
		{"没有前缀就不动", "/shop", "", "", "/shop"},

		// 上游挂在子路径上:前缀要插在**请求路径那一段**之前。拼成 /fr/api/... 会 404 ——
		// 上游根本没有 /fr/api 这条路由。
		{"上游挂子路径", "/api/shop", "/api", "/fr", "/api/fr/shop"},
		{"上游子路径带尾斜杠", "/api/shop", "/api/", "/fr", "/api/fr/shop"},

		// base 对不上 outPath 时退回"整体前置"——宁可拼错也不要静默丢语言，
		// 而且这条在真实拓扑里走不到（outPath 就是 base 拼出来的）。
		{"base 对不上则整体前置", "/shop", "/other", "/fr", "/fr/shop"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := proxyPathWithPrefix(c.out, c.base, c.prefix); got != c.want {
				t.Errorf("proxyPathWithPrefix(%q,%q,%q)=%q want %q", c.out, c.base, c.prefix, got, c.want)
			}
		})
	}
}
