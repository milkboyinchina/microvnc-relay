//go:build !linux

package telemetry

import (
	"net"
	"time"
)

// ExtractTCPInfoRTT is a no-op fallback on non-Linux platforms where Linux TCP_INFO is unavailable.
func ExtractTCPInfoRTT(_ net.Conn) (time.Duration, error) {
	return 0, nil
}
