package volts

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestStartPprof_RejectsNonLoopback(t *testing.T) {
	for _, addr := range []string{":0", "0.0.0.0:0", "[::]:0", "192.168.1.10:0", "example.com:0"} {
		if bound, err := startPprof(addr); err == nil {
			t.Errorf("startPprof(%q) bound %s, want rejection", addr, bound)
		}
	}
}

func TestStartPprof_ServesOnLoopback(t *testing.T) {
	bound, err := startPprof("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(bound, "127.0.0.1:") {
		t.Fatalf("bound %s, want 127.0.0.1", bound)
	}

	c := &http.Client{Timeout: 5 * time.Second}
	rsp, err := c.Get("http://" + bound + "/debug/pprof/heap?debug=1")
	if err != nil {
		t.Fatal(err)
	}
	defer rsp.Body.Close()
	body, _ := io.ReadAll(rsp.Body)
	if rsp.StatusCode != http.StatusOK || !strings.Contains(string(body), "heap profile") {
		t.Fatalf("status %d, body %.80q", rsp.StatusCode, body)
	}
}
