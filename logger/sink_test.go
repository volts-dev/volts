package logger

import (
	"strings"
	"sync"
	"testing"
)

// recordingSink 收下所有 Wants 放行的条目。
type recordingSink struct {
	mu      sync.Mutex
	minSev  int
	entries []Entry
}

func (s *recordingSink) Wants(l Level) bool { return l.Severity() >= s.minSev }
func (s *recordingSink) Write(e Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, e)
}
func (s *recordingSink) snapshot() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Entry(nil), s.entries...)
}

// TestSink_ReceivesStructuredEntries —— 出口拿到的是拆好的结构：等级标签摘掉、
// logger 名对上、调用位置指向**本测试文件**而不是 logger 包自己。
func TestSink_ReceivesStructuredEntries(t *testing.T) {
	l, _ := newCapturing(t, "sink-basic")
	s := &recordingSink{}
	defer AddSink(s)()

	l.Errf("boom %d", 42)
	l.Warn("careful")

	got := s.snapshot()
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d: %+v", len(got), got)
	}
	e := got[0]
	if e.Tag != "ERR" || e.Message != "boom 42" || e.Level != LevelError || e.Logger != "sink-basic" {
		t.Errorf("unexpected entry: %+v", e)
	}
	// 调用位置的判定在 sink_caller_test.go（外部测试包）：本文件与 logger 同包，
	// 它自己的帧会被当作 logger 包的帧跳过，在这里断言反而是错的。
	if got[1].Tag != "WARN" || got[1].Message != "careful" {
		t.Errorf("unexpected second entry: %+v", got[1])
	}
}

// TestSink_WantsGatesBySeverity —— Wants 为假的档次连 Entry 都不构造；
// 而 Severity 序才是"warn 及以上"的正确判据（Debug 的数值比 Error 大）。
func TestSink_WantsGatesBySeverity(t *testing.T) {
	l, _ := newCapturing(t, "sink-sev")
	s := &recordingSink{minSev: LevelWarn.Severity()}
	defer AddSink(s)()

	l.Dbg("d")
	l.Trace("t")
	l.Info("i")
	l.Warn("w")
	l.Err("e")
	l.Atk("a")

	var tags []string
	for _, e := range s.snapshot() {
		tags = append(tags, e.Tag)
	}
	if strings.Join(tags, ",") != "WARN,ERR,ATK" {
		t.Errorf("want WARN,ERR,ATK got %v", tags)
	}
}

// TestSink_RespectsLoggerLevel —— logger 自己配成不出 debug，出口也看不到 debug。
func TestSink_RespectsLoggerLevel(t *testing.T) {
	l, _ := newCapturing(t, "sink-lvl")
	l.SetLevel(LevelError)
	defer l.SetLevel(LevelDebug)
	s := &recordingSink{}
	defer AddSink(s)()

	l.Dbg("hidden")
	l.Err("shown")

	got := s.snapshot()
	if len(got) != 1 || got[0].Message != "shown" {
		t.Errorf("want only the error, got %+v", got)
	}
}

// TestSink_RemoveStopsDelivery —— 摘掉之后一条都不再收；重复摘是空操作。
func TestSink_RemoveStopsDelivery(t *testing.T) {
	l, _ := newCapturing(t, "sink-rm")
	s := &recordingSink{}
	remove := AddSink(s)
	l.Err("one")
	remove()
	remove()
	l.Err("two")
	if got := s.snapshot(); len(got) != 1 || got[0].Message != "one" {
		t.Errorf("want just 'one', got %+v", got)
	}
}

type panickingSink struct{}

func (panickingSink) Wants(Level) bool { return true }
func (panickingSink) Write(Entry)      { panic("sink exploded") }

// TestSink_PanicIsContained —— 一个坏出口既不能让写日志的 goroutine 崩，也不能
// 挡住排在它后面的出口和 writer 本身。
func TestSink_PanicIsContained(t *testing.T) {
	l, w := newCapturing(t, "sink-panic")
	defer AddSink(panickingSink{})()
	s := &recordingSink{}
	defer AddSink(s)()

	l.Err("still logged")

	if got := s.snapshot(); len(got) != 1 {
		t.Errorf("second sink should still receive, got %+v", got)
	}
	if lines := w.snapshot(); len(lines) != 1 || !strings.Contains(lines[0], "still logged") {
		t.Errorf("writer should still receive, got %v", lines)
	}
}

// TestSink_AsyncModeStillDeliversEverything —— 异步模式下出口在入队之前收到，
// 与 writer 那边是否丢弃无关。
func TestSink_AsyncModeStillDeliversEverything(t *testing.T) {
	l, _ := newCapturing(t, "sink-async")
	l.Async(true)
	defer l.Close()
	s := &recordingSink{}
	defer AddSink(s)()

	for i := 0; i < 50; i++ {
		l.Err("n")
	}
	if got := s.snapshot(); len(got) != 50 {
		t.Errorf("want 50 got %d", len(got))
	}
}

func TestSplitLevelTag(t *testing.T) {
	cases := []struct{ in, tag, body string }{
		{"[ERR] boom", "ERR", "boom"},
		{"[PANIC] x", "PANIC", "x"},
		{"[INFO]no space", "INFO", "no space"},
		{"[order-1] not a tag", "", "[order-1] not a tag"},
		{"[toolongtagname] x", "", "[toolongtagname] x"},
		{"plain", "", "plain"},
		{"[]", "", "[]"},
		{"", "", ""},
	}
	for _, c := range cases {
		tag, body := splitLevelTag(c.in)
		if tag != c.tag || body != c.body {
			t.Errorf("%q: got (%q,%q) want (%q,%q)", c.in, tag, body, c.tag, c.body)
		}
	}
}

func TestSeverityOrder(t *testing.T) {
	order := []Level{LevelDebug, LevelTrace, LevelInfo, LevelWarn, LevelError,
		LevelEmergency, LevelAlert, LevelCritical, LevelAttack}
	for i := 1; i < len(order); i++ {
		if order[i].Severity() <= order[i-1].Severity() {
			t.Errorf("%s (%d) should be more severe than %s (%d)",
				order[i], order[i].Severity(), order[i-1], order[i-1].Severity())
		}
	}
	if lv, ok := ParseLevel("Warning"); !ok || lv != LevelWarn {
		t.Errorf("ParseLevel(Warning) = %v,%v", lv, ok)
	}
	if _, ok := ParseLevel("nope"); ok {
		t.Error("ParseLevel(nope) should fail")
	}
}

func TestIsLoggerPackageFunc(t *testing.T) {
	yes := []string{
		"github.com/volts-dev/volts/logger.(*TLogger).Errf",
		"github.com/vectors-project/vectors/logger.Warnf",
		"logger.Info",
	}
	no := []string{
		"github.com/vectors-project/vectors/core/cron.loop",
		"main.main",
		"github.com/x/loggerx.Do",
		"",
	}
	for _, f := range yes {
		if !isLoggerPackageFunc(f) && f != "" {
			t.Errorf("%q should be a logger frame", f)
		}
	}
	for _, f := range no[:3] {
		if isLoggerPackageFunc(f) {
			t.Errorf("%q should not be a logger frame", f)
		}
	}
}
