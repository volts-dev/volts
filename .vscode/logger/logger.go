package logger

import (
	"fmt"

	log "github.com/volts-dev/logger"
)

var logger log.ILogger

// build a new logger for entire orm
func init() {
	logger = log.NewLogger(`{"Prefix":"VOLTS"}`)
}

// return the logger instance
func Logger() log.ILogger {
	return logger
}
r
func Lvl(level log.Level, log ...log.ILogger)) bool{
	logger.Lvl(level,log)
}
// 断言如果结果和条件不一致就错误
func Assert(cnd bool, format string, args ...interface{}) {
	if !cnd {
		panic(fmt.Sprintf(format, args...))
	}
}

func Atkf(fmt string, arg ...interface{}) {
	logger.Atkf(fmt, arg...)
}

func Info(err ...interface{}) {
	logger.Info(err...)
}

func Infof(fmt string, arg ...interface{}) {
	logger.Infof(fmt, arg...)
}

func Warn(err ...interface{}) {
	logger.Warn(err...)
}

func Warnf(fmt string, arg ...interface{}) {
	logger.Warnf(fmt, arg...)
}

func Dbg(err ...interface{}) {
	logger.Dbg(err...)
}

func Err(err ...interface{}) {
	logger.Err(err...)
}

func Errf(fmt string, arg ...interface{}) error {
	return logger.Errf(fmt, arg...)
}

func Panicf(format string, args ...interface{}) {
	panic(fmt.Sprintf(format, args...))
}

func PanicErr(err error, title ...string) bool {
	if err != nil {
		logger.Dbg(err)
		panic(err)
		//panic("[" + title[0] + "] " + err.Error())
		return true
	}
	return false
}

func LogErr(err error, title ...string) bool {
	if err != nil {
		//logger.ErrorLn(err)
		if len(title) > 0 {
			logger.Err("[" + title[0] + "] " + err.Error())
		} else {
			logger.Err(err.Error())
		}

		return true
	}
	return false
}
