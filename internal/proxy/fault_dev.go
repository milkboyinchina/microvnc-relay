//go:build dev

package proxy

import "sync/atomic"

var (
	faultRST  atomic.Bool
	faultDrop atomic.Bool
)

// SetFaultRST makes Mode 1 reject new sessions with a TCP reset.
func SetFaultRST(on bool) {
	faultRST.Store(on)
}

// SetFaultDrop makes Mode 1 accept and immediately close new sessions.
func SetFaultDrop(on bool) {
	faultDrop.Store(on)
}

func faultActive() (rst, drop bool) {
	return faultRST.Load(), faultDrop.Load()
}
