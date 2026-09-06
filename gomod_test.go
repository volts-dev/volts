package volts

import (
	"os"
	"strings"
	"testing"
)

// TestGoMod_HasNoReplaceDirective 钉住一条分发性质：**别人 go get 得下来**。
//
// go.mod 里的 `replace ... => ../xxx` 只在本机成立。它一旦留在仓库里，任何不在
// 这台机器上的人执行 `go get github.com/volts-dev/volts` 都会失败，而报出来的是
// 「找不到 ../cacher」这种指向对方磁盘的错误，跟"这个库坏了"没法区分。
//
// 这不是假想：cacher 的本地 replace 在这里挂了几个月，期间这个仓对外是拿不到的
// （见 CLAUDE.md）。临时 replace 本身是正当手段，但它必须是**临时**的 ——
// 这条测试让"忘了摘掉"立刻可见，而不是等到有人来报装不上。
func TestGoMod_HasNoReplaceDirective(t *testing.T) {
	src, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(string(src), "\n") {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(s, "//") {
			continue
		}
		if strings.HasPrefix(s, "replace ") || s == "replace (" {
			t.Errorf("go.mod:%d 有 replace 指令: %s\n"+
				"它只在本机成立，留在仓库里等于别人 go get 不下来。"+
				"上游发布后请摘掉并 bump 版本。", i+1, s)
		}
	}
}
