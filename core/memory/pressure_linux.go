package memory

import (
	"os"
	"path/filepath"
)

// readPressure 有共享 cgroup 时读那一层的 memory.pressure（只算这组进程），否则读整机的。
func readPressure(sharedDir string) Pressure {
	if sharedDir != "" {
		if b, err := os.ReadFile(filepath.Join(sharedDir, "memory.pressure")); err == nil {
			if p, ok := parsePSI(b); ok {
				return p
			}
		}
	}
	if b, err := os.ReadFile("/proc/pressure/memory"); err == nil {
		if p, ok := parsePSI(b); ok {
			return p
		}
	}
	return Pressure{}
}
