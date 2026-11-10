package transport

import (
	"net"
	"sync"
	"testing"
	"time"
)

func TestInprocCloseStateTracksEitherEnd(t *testing.T) {
	for _, side := range []string{"client", "server"} {
		t.Run(side, func(t *testing.T) {
			accepted := make(chan net.Conn, 1)
			client := dialInproc(func(c net.Conn) { accepted <- c }).(*inprocConn)
			t.Cleanup(func() { _ = client.Close() })
			var server *inprocConn
			select {
			case c := <-accepted:
				server = c.(*inprocConn)
			case <-time.After(time.Second):
				t.Fatal("pipe not accepted")
			}
			t.Cleanup(func() { _ = server.Close() })
			if client.IsClosed() || server.IsClosed() {
				t.Fatal("fresh pipe is closed")
			}
			closing := client
			if side == "server" {
				closing = server
			}
			if err := closing.Close(); err != nil {
				t.Fatal(err)
			}
			if !client.IsClosed() || !server.IsClosed() {
				t.Fatal("close was not visible at both ends")
			}
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Go(func() { _ = client.Close(); _ = server.Close(); _ = client.IsClosed() })
			}
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("concurrent close hung")
			}
		})
	}
}
