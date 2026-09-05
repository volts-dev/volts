package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNew_MalformedFileIsReported 锁定：配置文件本身解析不了时，错误必须进
// `Errors()`，而不是被静默吞掉。
//
// `New()` 末尾调 `cfg.LoadFromFile()` 却**丢掉了它的返回值**，而
// `defaultConfig = New(DEFAULT_PREFIX)` 就在包初始化里 —— 于是一份 JSON 写坏了的
// config.json 在进程启动时被完全忽略：所有配置停在代码默认值上，`config.Errors()`
// 是空的，日志里一个字都没有。这正是本仓最贵的那一类故障（"改了配置没反应"），
// 而且是它最极端的形态：整份文件都没生效。
//
// 本仓的既定做法是**累积而不是 log.Fatal**（库不该替调用方 os.Exit），
// 所以判据是"错误出现在 Errors() 里"，不是"启动失败"。
func TestNew_MalformedFileIsReported(t *testing.T) {
	fn := "malformed_cfg.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	// 少一个右花括号 —— 手改配置最常见的坏法
	if err := os.WriteFile(path, []byte(`{"server": {"port": 8080}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := newIsolated(t, fn)

	err := cfg.Errors()
	if err == nil {
		t.Fatal("配置文件解析失败却没有任何记录 —— 整份文件不生效，而调用方无从得知")
	}
	if !strings.Contains(err.Error(), fn) {
		t.Errorf("错误信息里应指出是哪个文件，实际: %v", err)
	}
}

// TestNew_GoodFileReportsNothing 反向断言：正常文件不能凭空产生错误。
func TestNew_GoodFileReportsNothing(t *testing.T) {
	fn := "wellformed_cfg.json"
	path := filepath.Join(AppPath, fn)
	defer os.Remove(path)

	if err := os.WriteFile(path, []byte(`{"lintsec": {"plain": "ok"}}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := newIsolated(t, fn)
	if err := cfg.Errors(); err != nil {
		t.Fatalf("正常配置文件不该产生错误: %v", err)
	}
}

// TestNew_MissingFileIsNotAnError 锁定"文件不存在"不算错误 —— 全部走代码默认值是
// 合法部署方式（AutoCreateFile 另有其事）。
func TestNew_MissingFileIsNotAnError(t *testing.T) {
	cfg := newIsolated(t, "definitely_absent_cfg.json")
	if err := cfg.Errors(); err != nil {
		t.Fatalf("文件不存在不该报错: %v", err)
	}
	defer os.Remove(filepath.Join(AppPath, "definitely_absent_cfg.json"))
}
