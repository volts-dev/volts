package memory

import "testing"

func TestParseMeminfo(t *testing.T) {
	in := "MemTotal:        3481600 kB\nMemFree:          120000 kB\nMemAvailable:     900000 kB\nBuffers:           10000 kB\n"
	h, ok := parseMeminfo([]byte(in))
	if !ok || h.Total != 3481600<<10 || h.Available != 900000<<10 {
		t.Fatalf("got %+v, %v", h, ok)
	}
	if _, ok := parseMeminfo([]byte("MemTotal: 3481600 kB\nMemFree: 1 kB\n")); ok {
		t.Fatal("没有 MemAvailable 的老内核不该猜")
	}
}
