package logger

import (
	"runtime"
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

// ------------------------------------------------------------
// 并发安全
// ------------------------------------------------------------

// 反复调用 Async 只能有一条 listen goroutine。
//
// 此前每次 Async 都无条件 `go listen()`（注释写着"避免多次运行 Go 程"却没有
// 任何保护），Async(false) 再 Async(true) 就多一条常驻协程，且多个 listener
// 会瓜分同一个通道的消息。
func TestAsyncStartsSingleListener(t *testing.T) {
	l, _ := newCapturing(t, "once")

	before := runtime.NumGoroutine()
	for i := 0; i < 10; i++ {
		l.Async(true)
		l.Async(false)
	}
	l.Async(true)
	t.Cleanup(l.Close)

	// 给协程调度留出窗口，避免统计到尚未退出的临时协程
	time.Sleep(50 * time.Millisecond)

	if delta := runtime.NumGoroutine() - before; delta > 1 {
		t.Fatalf("21 次 Async 调用后多出 %d 条 goroutine，期望至多 1 条", delta)
	}
}

// 异步通道满时必须丢弃而不是阻塞调用方。
//
// 日志是可丢的，请求不是 —— 阻塞发送会让写盘慢/writer 卡住直接反向拖垮业务。
func TestAsyncDropsInsteadOfBlocking(t *testing.T) {
	l, _ := newCapturing(t, "drop")
	l.SetLevel(LevelDebug)

	// 打开异步但**不启动** listener，通道无人消费，必然填满
	l.manager.asynchronous.Store(true)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < cap(l.manager.msg)+200; i++ {
			l.Infof("flood %d", i)
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("写日志被阻塞了 —— 通道满时必须丢弃而非等待")
	}

	if l.Dropped() == 0 {
		t.Fatal("通道已填满，Dropped() 应大于 0")
	}
}

// 并发读写等级/开关/writer 不得触发竞态或 map 并发读写致命错误。
// 需配合 -race 运行才有意义。
func TestConcurrentLoggingAndReconfigure(t *testing.T) {
	l, w := newCapturing(t, "race")
	l.SetLevel(LevelDebug)

	var wg sync.WaitGroup

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				l.Infof("msg %d-%d", n, j)
			}
		}(i)
	}

	// 一边写日志一边改配置：等级、调用深度、level writer
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 200; j++ {
			l.SetLevel(LevelDebug)
			l.SetLevel(LevelInfo)
			l.EnableFuncCallDepth(j%2 == 0)
			l.SetLogFuncCallDepth(2)
			l.SetLevelWriter(LevelWarn, w)
		}
	}()

	wg.Wait()
}

// Close 前必须把通道里的存量日志写出 —— 崩溃现场的最后几行往往最关键。
func TestCloseFlushesPending(t *testing.T) {
	l, w := newCapturing(t, "flush")
	l.SetLevel(LevelDebug)

	// 异步但不启动 listener，消息全部堆在通道里
	l.manager.asynchronous.Store(true)
	for i := 0; i < 5; i++ {
		l.Infof("pending-%d", i)
	}

	if len(w.snapshot()) != 0 {
		t.Fatal("尚未 flush，不应有输出")
	}

	l.Close()

	if got := len(w.snapshot()); got != 5 {
		t.Fatalf("Close 后写出 %d 条，want 5", got)
	}
}
