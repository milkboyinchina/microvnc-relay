//go:build linux

package telemetry

import (
	"net"
	"time"

	"golang.org/x/sys/unix"
)

type connUnwrapper interface {
	UnderlyingConn() net.Conn
}

// ExtractTCPInfoRTT reads the kernel smoothed RTT (in microseconds) from a Linux TCP socket
// using getsockopt(SOL_TCP, TCP_INFO) without transmitting any extra network packets.
func ExtractTCPInfoRTT(conn net.Conn) (time.Duration, error) {
	if conn == nil {
		return 0, nil
	}

	for {
		unwrapper, ok := conn.(connUnwrapper)
		if !ok {
			break
		}
		conn = unwrapper.UnderlyingConn()
		if conn == nil {
			return 0, nil
		}
	}

	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		return 0, nil
	}

	rawConn, err := tcpConn.SyscallConn()
	if err != nil {
		return 0, err
	}

	var (
		info    *unix.TCPInfo
		sockErr error
	)
	err = rawConn.Control(func(fd uintptr) {
		info, sockErr = unix.GetsockoptTCPInfo(int(fd), unix.SOL_TCP, unix.TCP_INFO)
	})
	if err != nil {
		return 0, err
	}
	if sockErr != nil {
		return 0, sockErr
	}
	if info == nil {
		return 0, nil
	}

	return time.Duration(info.Rtt) * time.Microsecond, nil
}
