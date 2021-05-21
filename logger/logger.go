package logger

import (
	"fmt"

	log "github.com/volts-dev/logger"
)

type (
	ILogger interface {
		log.ILogger
	}
)

var DefaultLogger log.ILogger

// build a new logger for entire orm
func init() {
	DefaultLogger = log.NewLogger(`{"Prefix":"VOLTS"}`)
}

// return the logger instance
func Logger() log.ILogger {
	return DefaultLogger
}

// Returns true if the given level is at or lower the current logger level
func Lvl(level log.Level, logger ...log.ILogger) bool {
	var l log.ILogger = DefaultLogger
	if len(logger) > 0 {
		l = logger[0]
	}
	return l.GetLevel() <= level
}

// 断言如果结果和条件不一致就错误
func Assert(cnd bool, format string, args ...interface{}) {
	if !cnd {
		panic(fmt.Sprintf(format, args...))
	}
}

func Atkf(fmt string, arg ...interface{}) {
	DefaultLogger.Atkf(fmt, arg...)
}

func Info(err ...interface{}) {
	DefaultLogger.Info(err...)
}

func Infof(fmt string, arg ...interface{}) {
	DefaultLogger.Infof(fmt, arg...)
}

func Warn(err ...interface{}) {
	DefaultLogger.Warn(err...)
}

func Warnf(fmt string, arg ...interface{}) {
	DefaultLogger.Warnf(fmt, arg...)
}

func Dbg(err ...interface{}) {
	DefaultLogger.Dbg(err...)
}

func Err(err ...interface{}) {
	DefaultLogger.Err(err...)
}

func Errf(fmt string, arg ...interface{}) error {
	return DefaultLogger.Errf(fmt, arg...)
}

func Panicf(format string, args ...interface{}) {
	panic(fmt.Sprintf(format, args...))
}

func PanicErr(err error, title ...string) bool {
	if err != nil {
		DefaultLogger.Dbg(err)
		panic(err)
		//panic("[" + title[0] + "] " + err.Error())
		return true
	}
	return false
}

func LogErr(err error, title ...string) bool {
	if err != nil {
		//DefaultLogger.ErrorLn(err)
		if len(title) > 0 {
			DefaultLogger.Err("[" + title[0] + "] " + err.Error())
		} else {
			DefaultLogger.Err(err.Error())
		}

		return true
	}
	return false
}
