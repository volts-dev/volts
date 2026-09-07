package logger

// sink.go —— 结构化日志出口（进程级）。
//
// # 这是给"把日志送去别处"的那一类消费者的接口，不是第二个 writer
//
// IWriter 拿到的是一行**已经排好版**的文本（`[prefix][ERR] …`，前面可能还带
// `[file:line]`），且是按 logger 逐个挂的：要收集一个进程的全部日志，得对每一个
// `New(prefix)` 出来的实例各挂一份 —— 而 logger 是各包按需 `New` 的，没有任何一处
// 知道全集。一个想"把 error 级以上送去集中日志"的组件，在 IWriter 这层做不到。
//
// Sink 补的正是这两点：
//
//   - **进程级**。AddSink 一次，本进程每一个 logger 的每一条都经过它。
//   - **结构化**。拿到的是 Entry：等级、logger 名、去掉等级标签后的正文、调用位置，
//     不用再从排好版的字符串里反解。
//
// # 三条约定
//
//   - **同步且必须廉价。** Sink.Write 在写日志的那个 goroutine 上执行，发生在
//     异步通道之前（要不然 Sink 只能拿到通道没丢的那一部分，而通道满时丢的正是
//     最忙、最值得看的那一段）。所以 Write 里只许做入队一类的事；要发网络、写盘，
//     自己开 goroutine。Write 里的 panic 被吞掉，不会连累业务。
//   - **Wants 是第一道闸。** 只有 Wants(level) 为真才会去算调用位置（runtime.Caller
//     不便宜）并构造 Entry。一个只收 warn 以上的 sink，在 info/debug 上的开销就是
//     一次原子读加一次接口调用。
//   - **logger 自己的等级过滤在 Sink 之前。** 配置成只出 error 的 logger，它的 info
//     不会到 Sink —— Sink 看到的就是"这个进程会打出来的那些"，不多不少。
//
// # 等级：用 Severity，别直接比 Level 的数值
//
// Level 的数值是 RFC5424 风格外加两个本地等级，**不是**按严重程度单调的：
// Info(5) < Warn(6) < Error(7) < Trace(8) < Debug(9)。"error 及以上"在这个数轴上
// 不是一段连续区间。Level.Severity() 给的才是严重程度序（debug 最低，attack 最高）。

import (
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type (
	// Entry 是一条日志的结构化形态。
	Entry struct {
		// Time 写日志的那一刻（真实瞬间，不是墙上时间口径）。
		Time time.Time
		// Level 原始等级；严重程度请用 Level.Severity()。
		Level Level
		// Tag 是各写入方法烙在正文开头的那个标签（ERR / WARN / INFO / DBG / TRACE /
		// ATK / PANIC / FATAL），已从 Message 里摘掉。它比 Level 多一点信息：Panic
		// 是以 Warn 等级写的、Fatal 是以 Alert 等级写的，只有 Tag 分得出来。
		Tag string
		// Logger 是 New(prefix) 那个 prefix（已小写）。
		Logger string
		// Message 正文，不含 `[prefix]`、不含等级标签、不含 `[file:line]`。
		Message string
		// File / Line 写日志的调用点（logger 包自身的帧已跳过）。取不到时为空/0。
		File string
		Line int
	}

	// Sink 是一个结构化日志出口。
	Sink interface {
		// Wants 报告这个出口要不要 level 这一档。为假时不构造 Entry。
		Wants(level Level) bool
		// Write 收下一条。同步执行，必须廉价、不能阻塞（见文件头）。
		Write(Entry)
	}
)

var (
	sinkMu sync.Mutex
	// sinks 是当前全部出口的不可变快照；读侧一次原子 Load，写侧整体替换。
	// 写日志的热路径上不能有锁。
	sinks atomic.Pointer[[]Sink]
)

// AddSink 挂一个进程级出口，返回摘掉它的函数。
//
// 挂在启动阶段最好，但运行期挂/摘也是安全的（快照替换）。同一个 Sink 挂两次就会
// 收到两次，这里不去重。
func AddSink(s Sink) (remove func()) {
	if s == nil {
		return func() {}
	}
	sinkMu.Lock()
	defer sinkMu.Unlock()
	var cur []Sink
	if p := sinks.Load(); p != nil {
		cur = *p
	}
	next := make([]Sink, 0, len(cur)+1)
	next = append(next, cur...)
	next = append(next, s)
	sinks.Store(&next)

	var once sync.Once
	return func() {
		once.Do(func() {
			sinkMu.Lock()
			defer sinkMu.Unlock()
			var cur []Sink
			if p := sinks.Load(); p != nil {
				cur = *p
			}
			out := make([]Sink, 0, len(cur))
			removed := false
			for _, x := range cur {
				if !removed && x == s {
					removed = true
					continue
				}
				out = append(out, x)
			}
			sinks.Store(&out)
		})
	}
}

// notifySinks 把一条日志交给每个想要它的出口。
//
// 只在有出口、且至少一个出口 Wants 时才付构造 Entry 的代价（含 runtime.Caller）。
// 单个出口的 panic 被吞掉：日志出口坏了不该让业务 goroutine 一起倒。
func (self *TWriterManager) notifySinks(level Level, msg string) {
	p := sinks.Load()
	if p == nil || len(*p) == 0 {
		return
	}
	wanted := false
	for _, s := range *p {
		if s.Wants(level) {
			wanted = true
			break
		}
	}
	if !wanted {
		return
	}

	tag, body := splitLevelTag(msg)
	e := Entry{
		Time:    time.Now(),
		Level:   level,
		Tag:     tag,
		Logger:  self.config.Name,
		Message: body,
	}
	e.File, e.Line = callerOutsideLogger()

	for _, s := range *p {
		if !s.Wants(level) {
			continue
		}
		func() {
			defer func() { _ = recover() }()
			s.Write(e)
		}()
	}
}

// splitLevelTag 把 "[ERR] 正文" 拆成 ("ERR", "正文")。
//
// 只认开头紧贴的、全大写字母的短标签——那是本包各写入方法烙上去的；业务自己写的
// "[order-123] …" 这种不动。
func splitLevelTag(msg string) (tag, body string) {
	if len(msg) < 3 || msg[0] != '[' {
		return "", msg
	}
	end := strings.IndexByte(msg, ']')
	if end <= 1 || end > 8 {
		return "", msg
	}
	for i := 1; i < end; i++ {
		if c := msg[i]; c < 'A' || c > 'Z' {
			return "", msg
		}
	}
	return msg[1:end], strings.TrimLeft(msg[end+1:], " ")
}

// callerOutsideLogger 找到第一个不属于 logger 包的调用帧。
//
// 不能用固定深度：本包的 Infof 上面可能还叠着上层项目自己的 logger 包装
// （vectors/logger 就是这么做的），固定深度在两种调用路径下必有一种指错行。
// 判据是"这一帧所在的包叫 logger"——用函数名里的包路径末段，与文件路径无关，
// 所以在任何 vendor / module cache 布局下都成立。
func callerOutsideLogger() (string, int) {
	var pcs [16]uintptr
	n := runtime.Callers(3, pcs[:]) // 0=Callers 1=本函数 2=notifySinks
	if n == 0 {
		return "", 0
	}
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		if !isLoggerPackageFunc(f.Function) {
			return f.File, f.Line
		}
		if !more {
			break
		}
	}
	return "", 0
}

// isLoggerPackageFunc 判断一个完整函数名（如 github.com/x/y/logger.(*TLogger).Infof）
// 是否属于某个名叫 logger 的包。
func isLoggerPackageFunc(fn string) bool {
	if fn == "" {
		return true
	}
	// 包路径与函数名之间是最后一个 '/' 之后的第一个 '.'。
	slash := strings.LastIndexByte(fn, '/')
	rest := fn[slash+1:]
	dot := strings.IndexByte(rest, '.')
	if dot < 0 {
		return false
	}
	return rest[:dot] == "logger"
}
