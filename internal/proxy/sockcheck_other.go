//go:build !linux

package proxy

import "net"

// isSocketAlive provides a non-Linux fallback that assumes active connection.
func isSocketAlive(conn net.Conn) bool {
	return conn != nil
}
