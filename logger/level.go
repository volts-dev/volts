package logger

import "runtime"

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
	LevelAttack Level = iota //# under attack
	LevelCritical
	LevelAlert
	LevelEmergency
	LevelNone  //# logger is close
	LevelInfo  //
	LevelWarn  //
	LevelError //
	LevelDebug //# under debug mode
)

func (l Level) Color(msg string) string {
	if runtime.GOOS == "windows" {
		return ""
	}

	color := ""
	switch l {
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
	default:
		color = White
	}
	return color + msg + Reset
}

func (l Level) String() string {
	switch l {
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
	}
	return ""
}
