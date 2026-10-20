package memory

import (
	"bufio"
	"bytes"
	"os"
	"strconv"
	"strings"
)

func readHost() (Host, bool) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return Host{}, false
	}
	return parseMeminfo(b)
}

// parseMeminfo 取 MemTotal 与 MemAvailable（kB）。老内核（3.14 之前）没有 MemAvailable，
// 这时不猜，当读不到。
func parseMeminfo(b []byte) (Host, bool) {
	var h Host
	var gotTotal, gotAvail bool
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		n, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			continue
		}
		switch name {
		case "MemTotal":
			h.Total, gotTotal = n<<10, true
		case "MemAvailable":
			h.Available, gotAvail = n<<10, true
		}
	}
	return h, gotTotal && gotAvail && h.Total > 0
}
