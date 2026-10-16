package logger

import "testing"

type countingStringer struct{ n *int }

func (c countingStringer) String() string { *c.n++; return "x" }

// 级别不收的日志不格式化参数（参数的 String() 一次都不调）；收的照常。
func TestDisabledLevelDoesNotFormat(t *testing.T) {
	l := New("admits_test")
	l.SetLevel(LevelInfo)
	n := 0
	arg := countingStringer{&n}
	l.Dbg("ctx", arg)
	l.Dbgf("ctx %v", arg)
	l.Trace(arg)
	l.Tracef("%v", arg)
	if n != 0 {
		t.Fatalf("调试级别关着时参数被格式化了 %d 次", n)
	}
	l.Info(arg)
	l.Warnf("%v", arg)
	if n != 2 {
		t.Fatalf("收的级别要照常格式化：%d", n)
	}
	if err := l.Errf("%v", arg); err == nil || n != 3 {
		t.Fatalf("Errf 的返回值要那条消息：%v %d", err, n)
	}
}
