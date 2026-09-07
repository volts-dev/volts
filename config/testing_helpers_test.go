package config

import (
	"os"
	"strconv"
	"sync/atomic"
	"testing"
)

// newIsolated 建一棵每次调用都全新的配置树。
//
// New(name) 按 name 复用内存中已有的 core —— 这是设计如此，生产里一个 name 就是一个
// 逻辑配置实体。但测试必须彼此隔离：复用 core 会把上一轮留在 override 层的值、
// 已绑定的结构体一并继承过来，`go test -count=2` 第二轮便开始互相干扰。
func newIsolated(t *testing.T, fn string) *Config {
	t.Helper()
	return New(t.Name()+"#"+strconv.FormatInt(testSeq.Add(1), 10), WithFileName(fn))
}

var testSeq atomic.Int64

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// rewriteAndWatch 模拟 WithWatcher 的真实路径：文件变动后 format.Watch 只做
// ReadInConfig 再回调 onChange(=Reload)，并不走 LoadFromFile —— 后者会顺带把新值
// 直接写进各结构体，那样就绕过了热重载要验证的那段逻辑。
func rewriteAndWatch(t *testing.T, cfg *Config, path, content string) {
	t.Helper()
	write(t, path, content)
	if err := cfg.Core().fmt.ReadInConfig(); err != nil {
		t.Fatal(err)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
