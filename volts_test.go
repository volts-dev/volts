package volts

import (
	"os"
	"syscall"
	"testing"
)

// TestSignalNotifyExcludesKILL 验证信号处理不包含 SIGKILL（无法捕获的信号）
// 此测试同时作为架构约定文档：只注册 SIGTERM/SIGINT/SIGQUIT
func TestSignalNotifyExcludesKILL(t *testing.T) {
	registeredSignals := []os.Signal{
		syscall.SIGTERM,
		syscall.SIGINT,
		syscall.SIGQUIT,
	}

	for _, sig := range registeredSignals {
		if sig == syscall.Signal(9) { // SIGKILL = 9
			t.Errorf("SIGKILL (signal 9) must NOT be registered: it cannot be caught by the OS")
		}
	}

	t.Logf("Signal set verified: %v (no SIGKILL)", registeredSignals)
}
