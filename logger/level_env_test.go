package logger

import (
	"fmt"
	"testing"
	"time"
)

func uniqueName(t *testing.T) string {
	return fmt.Sprintf("lvtest-%s-%d", t.Name(), time.Now().UnixNano())
}

// 没有任何配置时默认 Info：Debug / Trace 不出。此前默认 Debug，续租与 SQL 日志把云上
// syslog 一天灌到 8GB。
func TestDefaultLevelIsInfo(t *testing.T) {
	t.Setenv(EnvLevel, "")
	l := New(uniqueName(t))
	if got := l.GetLevel(); got != LevelInfo {
		t.Fatalf("默认等级 = %v，期望 info", got)
	}
	w := &captureWriter{}
	l.manager.writer = map[string]IWriter{"cap": w}
	l.Tracef("x")
	l.Dbgf("x")
	l.Infof("y")
	if n := len(w.lines); n != 1 {
		t.Fatalf("默认等级下写出 %d 条，期望只有 info 那 1 条", n)
	}
}

// 环境变量压过配置：部署出去的 config.json 每个日志器都带着早年持久化的 "level": 9。
func TestEnvLevelOverridesConfig(t *testing.T) {
	t.Setenv(EnvLevel, "warn")
	l := New(uniqueName(t), WithLevel(LevelDebug))
	if got := l.GetLevel(); got != LevelWarn {
		t.Fatalf("设了 %s=warn，等级却是 %v", EnvLevel, got)
	}

	// 文件装载之后也不许把它抬回去
	l.Config.Level = LevelDebug
	l.Config.AfterLoad()
	if got := l.GetLevel(); got != LevelWarn {
		t.Fatalf("AfterLoad 把环境变量的覆盖冲掉了: %v", got)
	}
	// 覆盖只在镜像上，不落进配置结构体（否则 SaveToFile 会把它当差异写进文件）
	if l.Config.Level != LevelDebug {
		t.Fatalf("环境变量的覆盖写进了 Config.Level: %v", l.Config.Level)
	}
}

func TestEnvLevelUnknownIsIgnored(t *testing.T) {
	t.Setenv(EnvLevel, "loud")
	if got := New(uniqueName(t)).GetLevel(); got != DefaultLevel {
		t.Fatalf("认不出的 %s 应当忽略，等级却是 %v", EnvLevel, got)
	}
}

// 配置文件装载 / 热重载改了 Level，热路径要跟着变。此前只在 New 那一刻读一次。
func TestAfterLoadAndReloadReachHotPath(t *testing.T) {
	t.Setenv(EnvLevel, "")
	l := New(uniqueName(t))

	l.Config.Level = LevelDebug
	if err := l.Config.AfterLoad(); err != nil {
		t.Fatal(err)
	}
	if got := l.GetLevel(); got != LevelDebug {
		t.Fatalf("AfterLoad 之后等级 = %v，期望 debug", got)
	}

	next := *l.Config
	next.Level = LevelError
	if err := l.Config.OnReload(&next); err != nil {
		t.Fatal(err)
	}
	if got := l.GetLevel(); got != LevelError || l.Config.Level != LevelError {
		t.Fatalf("热重载之后等级 = %v / 配置 %v，期望 error", got, l.Config.Level)
	}
}

// 非终端（go test 的 stdout 是管道）不上色；NO_COLOR / FORCE_COLOR 可覆盖。
func TestConsoleColorOnlyOnTerminal(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "")
	if NewConsoleWriter().color {
		t.Fatal("stdout 不是终端却上色了（systemd 下转义码会原样进 syslog）")
	}
	t.Setenv("FORCE_COLOR", "1")
	if !NewConsoleWriter().color {
		t.Fatal("FORCE_COLOR 没生效")
	}
	t.Setenv("NO_COLOR", "1")
	if NewConsoleWriter().color {
		t.Fatal("NO_COLOR 应当压过 FORCE_COLOR")
	}
}
