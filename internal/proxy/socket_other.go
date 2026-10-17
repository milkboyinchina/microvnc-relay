//go:build !linux

package proxy

import (
	"net"
	"time"
)

// setTCPUserTimeout is a no-op on platforms without Linux TCP_USER_TIMEOUT.
func setTCPUserTimeout(conn *net.TCPConn, timeout time.Duration) error {
	return nil
}
