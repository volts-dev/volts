package logger

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// captureWriter 收集写入的日志行，供断言使用。
// 异步模式下 Write 由 listen goroutine 调用，故加锁。
type captureWriter struct {
	mu    sync.Mutex
	lines []string
}

func (w *captureWriter) Init(config string) error { return nil }
func (w *captureWriter) Destroy()                 {}

func (w *captureWriter) Write(level Level, msg string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lines = append(w.lines, msg)
	return nil
}

func (w *captureWriter) snapshot() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.lines...)
}

func (w *captureWriter) reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lines = nil
}

// newCapturing 建一个只写到 captureWriter 的 logger。
// 直接改 manager.writer 是白盒做法 —— 公开的 SetWriter 只认全局注册过的
// creator 名，测试不该为了拿到输出去污染全局注册表。
func newCapturing(t *testing.T, prefix string) (*TLogger, *captureWriter) {
	t.Helper()

	l := New(prefix)
	w := &captureWriter{}
	l.manager.writer = map[string]IWriter{"capture": w}
	return l, w
}

// 各等级的过滤关系必须成立：write() 的判据是 level > config.Level 则丢弃，
// 而等级数值是 Info(5) < Warn(6) < Error(7) < Debug(9) —— 数值大的更"啰嗦"。
// 这条断言把这个反直觉的顺序钉死，避免后人"顺手修正"成常见的反向定义。
func TestLoggerLevelFiltering(t *testing.T) {
	cases := []struct {
		name     string
		level    Level
		wantSeen []string
		wantGone []string
	}{
		{
			name:     "Debug 放行全部",
			level:    LevelDebug,
			wantSeen: []string{"d-msg", "e-msg", "i-msg", "w-msg"},
		},
		{
			name:     "Error 挡掉 Debug",
			level:    LevelError,
			wantSeen: []string{"e-msg", "i-msg", "w-msg"},
			wantGone: []string{"d-msg"},
		},
		{
			name:     "Warn 挡掉 Debug/Error",
			level:    LevelWarn,
			wantSeen: []string{"i-msg", "w-msg"},
			wantGone: []string{"d-msg", "e-msg"},
		},
		{
			name:     "Info 只放行 Info",
			level:    LevelInfo,
			wantSeen: []string{"i-msg"},
			wantGone: []string{"d-msg", "e-msg", "w-msg"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l, w := newCapturing(t, "lvl")
			l.SetLevel(c.level)

			l.Dbgf("%s", "d-msg")
			l.Errf("%s", "e-msg")
			l.Infof("%s", "i-msg")
			l.Warnf("%s", "w-msg")

			got := strings.Join(w.snapshot(), "\n")
			for _, want := range c.wantSeen {
				if !strings.Contains(got, want) {
					t.Errorf("level=%v 应输出 %q，实际输出:\n%s", c.level, want, got)
				}
			}
			for _, gone := range c.wantGone {
				if strings.Contains(got, gone) {
					t.Errorf("level=%v 不应输出 %q，实际输出:\n%s", c.level, gone, got)
				}
			}
		})
	}
}

// 每行必须带上 logger 的前缀名，多 logger 混写时靠它区分来源。
func TestLoggerWritesPrefix(t *testing.T) {
	l, w := newCapturing(t, "mysvc")
	l.SetLevel(LevelDebug)

	l.Infof("hello")

	lines := w.snapshot()
	if len(lines) != 1 {
		t.Fatalf("期望 1 行，得到 %d 行: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "[mysvc]") {
		t.Errorf("缺少前缀 [mysvc]: %q", lines[0])
	}
}

// EnableFuncCallDepth 打开后应带上调用点的 文件:行号。
func TestLoggerFuncCallDepth(t *testing.T) {
	l, w := newCapturing(t, "depth")
	l.SetLevel(LevelDebug)
	l.EnableFuncCallDepth(true)

	l.Infof("with-caller")

	lines := w.snapshot()
	if len(lines) != 1 {
		t.Fatalf("期望 1 行，得到 %d 行: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "logger_test.go:") {
		t.Errorf("未带调用点信息: %q", lines[0])
	}
}

// 异步模式下日志最终必须被投递出去。
//
// 本测试同时是一道防线：旧版 TestLogger 以 `<-make(chan int)` 结尾，永久阻塞，
// 使整个 logger 包在 CI 上必然超时（实测 307s），进而掩盖本包所有回归。
// 任何新增的异步用例都必须自带超时退出，绝不能无限等待。
func TestLoggerAsyncDelivers(t *testing.T) {
	l, w := newCapturing(t, "async")
	l.SetLevel(LevelDebug)
	l.Async()

	l.Infof("async-msg")

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range w.snapshot() {
			if strings.Contains(line, "async-msg") {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("3s 内未投递异步日志，实际收到: %v", w.snapshot())
}
