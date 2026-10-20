package memory

import "golang.org/x/sys/unix"

func readPressure(string) Pressure {
	v, err := unix.SysctlUint32("kern.memorystatus_vm_pressure_level")
	if err != nil {
		return Pressure{}
	}
	return darwinPressure(v)
}
