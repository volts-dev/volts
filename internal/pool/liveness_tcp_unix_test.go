//go:build darwin || linux

package pool

import (
	"net"
	"testing"
	"time"
)

func probeTCPPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_ = ln.SetDeadline(time.Now().Add(time.Second))
	client, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server, err := ln.AcceptTCP()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return client.(*net.TCPConn), server
}

func TestTCPProbePreservesReadDeadline(t *testing.T) {
	client, _ := probeTCPPair(t)
	_ = client.SetReadDeadline(time.Now().Add(-time.Second))
	if closed, supported := tcpClosedByPeer(client); closed || !supported {
		t.Fatalf("healthy TCP connection: closed=%v supported=%v", closed, supported)
	}
	var buf [1]byte
	_, err := client.Read(buf[:])
	if e, ok := err.(net.Error); !ok || !e.Timeout() {
		t.Fatalf("probe changed the caller's read deadline: %v", err)
	}
}

func TestTCPProbeRejectsClosedAndUnsolicitedData(t *testing.T) {
	for _, mode := range []string{"fin", "reset", "local_close", "data"} {
		t.Run(mode, func(t *testing.T) {
			client, server := probeTCPPair(t)
			switch mode {
			case "fin":
				_ = server.Close()
			case "reset":
				_ = server.SetLinger(0)
				_ = server.Close()
			case "local_close":
				_ = client.Close()
			case "data":
				_ = server.SetWriteDeadline(time.Now().Add(time.Second))
				if _, err := server.Write([]byte("x")); err != nil {
					t.Fatal(err)
				}
			}
			deadline := time.Now().Add(time.Second)
			for {
				closed, supported := tcpClosedByPeer(client)
				if !supported {
					t.Fatal("native TCP probe unsupported")
				}
				if closed {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("unusable connection remained eligible for reuse")
				}
				time.Sleep(time.Millisecond)
			}
			if mode == "data" {
				_ = client.SetReadDeadline(time.Now().Add(time.Second))
				var buf [1]byte
				if n, err := client.Read(buf[:]); n != 1 || err != nil || buf[0] != 'x' {
					t.Fatalf("probe consumed application bytes: n=%d data=%q err=%v", n, buf, err)
				}
			}
		})
	}
}

func TestTCPProbeDoesNotUnwrapConnections(t *testing.T) {
	client, _ := probeTCPPair(t)
	// TLS/proxy wrappers must retain their own Read semantics.
	if _, supported := tcpClosedByPeer(struct{ net.Conn }{client}); supported {
		t.Fatal("probe bypassed connection wrapper")
	}
}
