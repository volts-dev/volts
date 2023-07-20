package test

import "github.com/volts-dev/volts/logger"

var log = logger.New("Test")

func Logger() logger.ILogger {
	return log
}
