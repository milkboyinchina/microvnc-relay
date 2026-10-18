//go:build !linux

package proxy

import (
	"net"
	"time"
)

// spliceMetered is unavailable outside Linux; callers fall back to userspace
// copying while preserving metering and half-close behavior.
func spliceMetered(
	dst *net.TCPConn,
	src *net.TCPConn,
	onBytes func(uint64),
	idleTimeout time.Duration,
) (handled bool, total int64, err error) {
	spliceFallbacks.Add(1)
	return false, 0, nil
}
