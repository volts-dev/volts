package logger_test

// 调用位置的回归必须住在**外部测试包**：Entry.File 的规则是"跳过所有属于 logger 包
// 的帧"，而 package logger 里的测试函数自己就是这样的帧 —— 在那里断言只会得到
// testing.go。这里的包名是 logger_test，函数名的包段对不上 "logger"，才是一个
// 真实调用方的形状。

import (
	"strings"
	"sync"
	"testing"

	"github.com/volts-dev/volts/logger"
)

type callerSink struct {
	mu   sync.Mutex
	last logger.Entry
	seen bool
}

func (s *callerSink) Wants(logger.Level) bool { return true }
func (s *callerSink) Write(e logger.Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last, s.seen = e, true
}

func TestSink_CallerPointsAtTheCallSite(t *testing.T) {
	s := &callerSink{}
	defer logger.AddSink(s)()

	l := logger.New("sink-caller-ext")
	l.Errf("where am I") // ← 这一行

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.seen {
		t.Fatal("sink received nothing")
	}
	if !strings.HasSuffix(s.last.File, "sink_caller_test.go") || s.last.Line == 0 {
		t.Errorf("caller should be this file, got %s:%d", s.last.File, s.last.Line)
	}
}
