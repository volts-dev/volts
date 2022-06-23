package logger

import (
	"fmt"
	"testing"
)

func TestLogger(t *testing.T) {
	log := New("log")
	fmt.Println("Strart")

	log.SetLevel(LevelDebug)
	log.Async()
	log.EnableFuncCallDepth(true)
	log.Dbgf("%s", "Test LevelDebug!")
	log.Errf("%s", "Test LevelDebug!")
	log.Infof("%s", "Test LevelDebug!")
	log.Warnf("%s", "Test LevelDebug!")

	log.SetLevel(LevelError)
	log.Dbgf("%s", "Test LevelError!")
	log.Errf("%s", "Test LevelError!")
	log.Infof("%s", "Test LevelError!")
	log.Warnf("%s", "Test LevelError!")

	log.SetLevel(LevelWarn)
	log.Dbgf("%s", "Test LevelWarn!")
	log.Errf("%s", "Test LevelWarn!")
	log.Infof("%s", "Test LevelWarn!")
	log.Warnf("%s", "Test LevelWarn!")

	log.SetLevel(LevelInfo)
	log.Dbgf("%s", "Test logger!")
	log.Errf("%s", "Test logger!")
	log.Infof("%s", "Test logger!")
	log.Warnf("%s", "Test logger!")

	fmt.Println("end")
	<-make(chan int)
}
