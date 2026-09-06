package config

import (
	"os"
	"regexp"
	"testing"
)

// TestHotFields_AreNotReadDirectlyOnRequestPaths 钉住一份清单：
// **会被热重载改写、又在请求/连接路径上被读**的配置字段，读的时候必须走访问器
// （访问器持 HotRLock），不能直接读结构体字段。
//
// 为什么要有这条守卫：竞态是不可见的。直接读一个 bool 在主流架构上永远"能跑"，
// 只有 -race 才看得见，而请求路径上的竞态未必每次跑测试都撞上。把它变成一条
// 源码扫描，新增一处直接读就立刻变红。
//
// 判据刻意定成「这些文件里不许出现这些字段名」而不是「全仓不许」：装配期读同一个
// 字段是完全正当的（那时候还没有请求在跑），禁掉会逼人绕路。
//
// transport 与 server 的配置**不在**这张表里，不是漏了：它们的 OnReload 直接返回
// ErrReloadUnsupported，整节退出热重载，写者根本不存在，竞态已经是零。那是另一条
// 同样正当的路 —— 监听地址、超时这类东西改了本就要重启。给它们加读锁只会守一个
// 不存在的写者。加新热字段之前先确认那个模型走的是哪条路。
//
// 加新的热字段时把它加进这张表 —— 表本身就是那份清单。
func TestHotFields_AreNotReadDirectlyOnRequestPaths(t *testing.T) {
	// file -> 该文件里不允许出现的字段读法
	banned := map[string][]string{
		"../router/router.go":     {"config.Recover", "config.RequestPrinter"},
		"../selector/registry.go": {"config.BreakerFailures", "config.BreakerCooldown", "config.BreakerMaxCooldown"},
		"../selector/breaker.go":  {"cfg.BreakerFailures", "cfg.BreakerCooldown", "cfg.BreakerMaxCooldown"},
	}

	for file, fields := range banned {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("读 %s: %v —— 文件被改名或移走了，这条守卫也要跟着改", file, err)
		}
		text := string(src)
		for _, f := range fields {
			// 必须是全词匹配：RecoverHandler 是 `field:"-"`（热重载不碰它），
			// 前缀匹配会把它误判成 Recover。
			re := regexp.MustCompile(`\b` + regexp.QuoteMeta(f) + `\b`)
			if re.MatchString(text) {
				t.Errorf("%s 里直接读了 %s。它会被热重载改写，且这个文件在请求/连接路径上 —— "+
					"要走持 config.HotRLock 的访问器（如 RecoverEnabled / breakerSettings / dialTimeouts）", file, f)
			}
		}
	}
}
