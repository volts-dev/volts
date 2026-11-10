//go:build darwin || linux

package pool

import (
	"net"

	"golang.org/x/sys/unix"
)

// Peek once while RawConn holds the descriptor open. EAGAIN means a healthy idle
// socket; EOF, errors or unsolicited RPC bytes mean it cannot be reused.
// MSG_DONTWAIT ensures Control never parks waiting for an idle peer to send data.
func tcpClosedByPeer(conn net.Conn) (closed, supported bool) {
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		return false, false
	}
	raw, err := tcp.SyscallConn()
	if err != nil {
		return true, true
	}
	err = raw.Control(func(fd uintptr) {
		var buf [1]byte
		for {
			_, _, err := unix.Recvfrom(int(fd), buf[:], unix.MSG_PEEK|unix.MSG_DONTWAIT)
			if err == unix.EINTR {
				continue
			}
			closed = err != unix.EAGAIN && err != unix.EWOULDBLOCK
			return
		}
	})
	return err != nil || closed, true
}
