//go:build linux

package proxy

import (
	"errors"
	"net"

	"golang.org/x/sys/unix"
)

// isSocketAlive checks non-blocking whether an idle TCP socket has been closed by the peer
// using MSG_PEEK | MSG_DONTWAIT without consuming any payload bytes.
func isSocketAlive(conn net.Conn) bool {
	if conn == nil {
		return false
	}
	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		return true
	}
	rawConn, err := tcpConn.SyscallConn()
	if err != nil {
		return true
	}

	alive := true
	_ = rawConn.Read(func(fd uintptr) bool {
		var buf [1]byte
		n, _, err := unix.Recvfrom(int(fd), buf[:], unix.MSG_PEEK|unix.MSG_DONTWAIT)
		if err != nil {
			if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
				alive = true
			} else {
				alive = false
			}
		} else if n == 0 {
			// EOF (remote peer closed socket)
			alive = false
		} else {
			// Data waiting in socket buffer, connection alive
			alive = true
		}
		return true
	})
	return alive
}
