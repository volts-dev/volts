//go:build !linux && !darwin && !windows

package memory

func readHost() (Host, bool) { return Host{}, false }

func readPressure(string) Pressure { return Pressure{} }
