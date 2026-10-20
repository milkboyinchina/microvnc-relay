package proxy

import "sync/atomic"

var (
	spliceDirections atomic.Uint64
	spliceFallbacks  atomic.Uint64
)

// SpliceStats reports how many stream directions have used the kernel splice
// path and how many fell back to userspace copying. It is informational and
// available on every platform.
func SpliceStats() (directions, fallbacks uint64) {
	return spliceDirections.Load(), spliceFallbacks.Load()
}
