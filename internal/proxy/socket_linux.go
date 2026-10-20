//go:build linux

package proxy

import (
	"net"
	"time"

	"golang.org/x/sys/unix"
)

// setTCPUserTimeout configures Linux TCP_USER_TIMEOUT. It bounds how long
// unacknowledged transmitted data may remain before the kernel fails the
// connection, preventing dead peers from pinning goroutines and FDs forever.
func setTCPUserTimeout(conn *net.TCPConn, timeout time.Duration) error {
	if conn == nil || timeout <= 0 {
		return nil
	}

	rawConn, err := conn.SyscallConn()
	if err != nil {
		return err
	}

	ms := int(timeout / time.Millisecond)
	if ms <= 0 {
		ms = 1
	}

	var sockErr error
	if err := rawConn.Control(func(fd uintptr) {
		sockErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_USER_TIMEOUT, ms)
	}); err != nil {
		return err
	}
	return sockErr
}
