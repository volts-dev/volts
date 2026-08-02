package accept_language

import "testing"

func TestParse_QualityOrderingAndRejection(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   []string
	}{
		{"空头", "", nil},
		{"单个", "fr", []string{"fr"}},
		{"按 q 降序", "en;q=0.3, fr;q=0.9, de;q=0.5", []string{"fr", "de", "en"}},
		{"缺省 q 为 1", "en;q=0.5, fr", []string{"fr", "en"}},
		// 同 q 保持书写顺序:客户端惯例是按优先级书写,打乱等于丢信息。
		{"同 q 稳定", "en, fr, de", []string{"en", "fr", "de"}},
		// q=0 是"绝不要这门语言",不是"优先级最低"。
		{"q=0 剔除", "de, en;q=0", []string{"de"}},
		{"q=0.0 剔除", "de, en;q=0.0", []string{"de"}},
		{"全部 q=0", "en;q=0", nil},
		// 坏 q 不该让整串偏好失效。
		{"q 不可解析按 1", "en;q=abc, fr;q=0.5", []string{"en", "fr"}},
		{"多余参数忽略", "en;charset=utf-8;q=0.2, fr;q=0.1", []string{"en", "fr"}},
		{"空段跳过", "en,,  ,fr", []string{"en", "fr"}},
		{"只有分号", ";q=0.5, fr", []string{"fr"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Parse(c.header)
			if len(got) != len(c.want) {
				t.Fatalf("Parse(%q) 得到 %v,want %v", c.header, got, c.want)
			}
			for i := range got {
				if got[i].Language != c.want[i] {
					t.Fatalf("Parse(%q)[%d]=%q,want %q", c.header, i, got[i].Language, c.want[i])
				}
			}
		})
	}
}

func TestNegotiate(t *testing.T) {
	full := []string{"en_US", "zh_CN", "zh_TW", "fr_FR", "de_DE"}

	cases := []struct {
		name      string
		header    string
		supported []string
		want      string
	}{
		{"精确(下划线 vs 连字符)", "zh-CN", full, "zh_CN"},
		{"截断到基语言", "fr-CA", full, "fr_FR"},
		{"基语言命中更具体的", "fr", full, "fr_FR"},

		// 同一门语言的两个地区变体都支持时,必须先精确匹配再退到基语言;
		// 否则按 supported 存放顺序会把繁体用户丢进简体界面。
		{"精确优先于同基语言", "zh-TW", full, "zh_TW"},
		{"精确优先(反向存放)", "zh-TW", []string{"zh_CN", "zh_TW"}, "zh_TW"},

		// 偏好顺序优先于精确度(RFC 4647 Lookup):用户把中文排在英文前,
		// 给他简体中文比给英文更接近意图。
		{"偏好顺序优先于精确度", "zh-TW, en-US", []string{"en_US", "zh_CN"}, "zh_CN"},
		{"按 q 值定偏好顺序", "en-US;q=0.5, zh-TW;q=0.9", []string{"en_US", "zh_CN"}, "zh_CN"},

		{"多级截断", "zh-Hant-TW", []string{"en_US", "zh_CN"}, "zh_CN"},
		{"通配给首项", "*", full, "en_US"},
		{"通配在低 q 位", "ja;q=0.9, *;q=0.1", full, "en_US"},

		{"无匹配", "ja, ko", full, ""},
		{"空头", "", full, ""},
		{"空支持集", "en", nil, ""},
		{"拒绝后无候选", "en;q=0", []string{"en_US"}, ""},

		// tag 前缀必须落在子标签边界上:zh 不该命中 zho。
		{"前缀不跨子标签边界", "zh", []string{"zho_XX"}, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Negotiate(c.header, c.supported); got != c.want {
				t.Fatalf("Negotiate(%q, %v)=%q,want %q", c.header, c.supported, got, c.want)
			}
		})
	}
}

// Negotiate 必须原样返回 supported 里的元素,而不是归一化后的形式——上层拿它
// 直接去查 res.lang.code / 索引译文表,大小写和下划线不能被改掉。
func TestNegotiate_ReturnsSupportedVerbatim(t *testing.T) {
	if got := Negotiate("zh-cn", []string{"zh_CN"}); got != "zh_CN" {
		t.Fatalf("Negotiate 返回 %q,want 原样 %q", got, "zh_CN")
	}
}
