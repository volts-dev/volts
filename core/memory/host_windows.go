package memory

import (
	"syscall"
	"unsafe"
)

// memoryStatusEx 是 MEMORYSTATUSEX。x/sys/windows 没有包这个调用，直接走 kernel32。
type memoryStatusEx struct {
	length               uint32
	memoryLoad           uint32 // 0–100，物理内存使用率
	totalPhys            uint64
	availPhys            uint64
	totalPageFile        uint64
	availPageFile        uint64
	totalVirtual         uint64
	availVirtual         uint64
	availExtendedVirtual uint64
}

var procGlobalMemoryStatusEx = syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

func globalMemoryStatus() (memoryStatusEx, bool) {
	var m memoryStatusEx
	m.length = uint32(unsafe.Sizeof(m))
	if r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m))); r == 0 {
		return memoryStatusEx{}, false
	}
	return m, true
}

func readHost() (Host, bool) {
	m, ok := globalMemoryStatus()
	if !ok || m.totalPhys == 0 {
		return Host{}, false
	}
	return Host{Total: m.totalPhys, Available: m.availPhys}, true
}
