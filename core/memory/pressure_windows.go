package memory

func readPressure(string) Pressure {
	m, ok := globalMemoryStatus()
	if !ok {
		return Pressure{}
	}
	return windowsPressure(m.memoryLoad)
}
