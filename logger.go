package volts

import (
	log "github.com/VectorsOrigin/logger"
)

/*
	logger 负责Web框架的日志打印
	@不提供给其他程序使用
*/
type (
	ILogger interface {
		GetLevel() int
		SetLevel(l int)

		Panicf(format string, v ...interface{})
		Dbgf(format string, v ...interface{})
		Atkf(format string, v ...interface{})
		Errf(format string, v ...interface{}) error
		Warnf(format string, v ...interface{})
		Infof(format string, v ...interface{})

		Panic(v ...interface{})
		Dbg(v ...interface{})
		Atk(v ...interface{})
		Err(v ...interface{}) error
		Warn(v ...interface{})
		Info(v ...interface{})
	}
)

var (
	logger = log.NewLogger("")
)

func init() {
}

func Logger() *log.TLogger {
	return logger
}
