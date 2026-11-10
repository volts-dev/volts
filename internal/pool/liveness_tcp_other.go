//go:build !darwin && !linux

package pool

import "net"

func tcpClosedByPeer(net.Conn) (bool, bool) { return false, false }
