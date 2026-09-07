package logger

import (
	"runtime"
	"strings"
)

type Level int8
type Brush func(string) string

var Reset = "\033[0m"
var Red = "\033[31m"
var Green = "\033[32m"
var Yellow = "\033[33m"
var Blue = "\033[34m"
var Purple = "\033[35m"
var Cyan = "\033[36m"
var Gray = "\033[37m"
var White = "\033[97m"

// RFC5424 log message levels.
const (
	LevelAttack    Level = iota // 0 under attack
	LevelCritical               // 1
	LevelAlert                  // 2
	LevelEmergency              // 3
	LevelNone                   // 4 logger is close
	LevelInfo                   // 5
	LevelWarn                   // 6
	LevelError                  // 7
	LevelTrace                  // 8 跟踪
	LevelDebug                  // 9 under debug mode
)

// Severity 把等级折成**严重程度序**：debug 最低、attack 最高。
//
// Level 的数值本身不是这个序（Info(5) < Warn(6) < Error(7) < Trace(8) < Debug(9)），
// 所以"error 及以上"不能写成 `level >= LevelError` —— 那会把 trace/debug 一起算进去、
// 把 info 之下的 alert/critical 排出去。凡是按严重程度筛选的地方（日志出口、
// 集中日志的最低等级）都该比这个值。
//
//	debug 0 · trace 1 · info 2 · none 2 · warn 3 · error 4 ·
//	emergency 5 · alert 6 · critical 7 · attack 8
func (lv Level) Severity() int {
	switch lv {
	case LevelDebug:
		return 0
	case LevelTrace:
		return 1
	case LevelInfo, LevelNone:
		return 2
	case LevelWarn:
		return 3
	case LevelError:
		return 4
	case LevelEmergency:
		return 5
	case LevelAlert:
		return 6
	case LevelCritical:
		return 7
	case LevelAttack:
		return 8
	}
	return 2
}

// ParseLevel 按名字解析等级（大小写不敏感；接受 String() 的输出以及常见别名
// warning / err / dbg / critical…）。认不出返回 false。
func ParseLevel(name string) (Level, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug", "dbg":
		return LevelDebug, true
	case "trace":
		return LevelTrace, true
	case "info", "information":
		return LevelInfo, true
	case "warn", "warning":
		return LevelWarn, true
	case "error", "err":
		return LevelError, true
	case "emergency":
		return LevelEmergency, true
	case "alert":
		return LevelAlert, true
	case "critical", "crit":
		return LevelCritical, true
	case "attack", "atk":
		return LevelAttack, true
	case "none":
		return LevelNone, true
	}
	return LevelNone, false
}

func (lv Level) Color(msg string) string {
	if runtime.GOOS == "windows" {
		return ""
	}

	color := ""
	switch lv {
	case LevelAttack:
		color = Red
	case LevelCritical:
		color = Red
	case LevelAlert:
		color = Yellow
	case LevelEmergency:
		color = Yellow
	case LevelInfo:
		color = White
	case LevelWarn:
		color = Yellow
	case LevelError:
		color = Red
	case LevelDebug:
		color = Purple
	case LevelTrace:
		color = Blue
	default:
		color = White
	}
	return color + msg + Reset
}

func (lv Level) String() string {
	switch lv {
	case LevelAttack:
		return "attack"
	case LevelCritical:
		return "critical"
	case LevelAlert:
		return "alert"
	case LevelEmergency:
		return "emergency"
	case LevelInfo:
		return "info"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	case LevelDebug:
		return "debug"
	case LevelTrace:
		return "trace"
	}
	return "unknow"
}
