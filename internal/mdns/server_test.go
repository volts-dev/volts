package mdns

import (
	"testing"
	"time"
)

func TestServer_StartStop(t *testing.T) {
	s := makeService(t)
	serv, err := NewServer(&Config{Zone: s, LocalhostChecking: true})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	defer serv.Shutdown()
}

func TestServer_Lookup(t *testing.T) {
	serv, err := NewServer(&Config{Zone: makeServiceWithServiceName(t, "_foobar._tcp"), LocalhostChecking: true})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	defer serv.Shutdown()

	entries := make(chan *ServiceEntry, 1)
	found := false
	doneCh := make(chan struct{})
	go func() {
		select {
		case e := <-entries:
			if e.Name != "hostname._foobar._tcp.local." {
				t.Logf("bad: %v", e)
				return
			}
			if e.Port != 80 {
				t.Logf("bad: %v", e)
				return
			}
			if e.Info != "Local web server" {
				t.Logf("bad: %v", e)
				return
			}
			found = true

		case <-time.After(80 * time.Millisecond):
			t.Logf("timeout")
			return
		}
		close(doneCh)
	}()

	params := &QueryParam{
		Service: "_foobar._tcp",
		Domain:  "local",
		Timeout: 50 * time.Millisecond,
		Entries: entries,
	}
	err = Query(params)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	<-doneCh
	if !found {
		t.Fatalf("record not found")
	}
}
