package proxy

import (
	"net"
	"sync"
	"sync/atomic"

	"github.com/milkboyinchina/microvnc-relay/internal/config"
)

const defaultPendingHandshakes = 64

// Semaphore is a fixed-capacity non-blocking admission gate used for
// accepted-but-unauthenticated sockets.
type Semaphore struct {
	ch       chan struct{}
	rejected atomic.Uint64
}

// NewSemaphore creates a semaphore with the given capacity. A non-positive
// capacity falls back to the safe default.
func NewSemaphore(capacity int) *Semaphore {
	if capacity <= 0 {
		capacity = defaultPendingHandshakes
	}
	return &Semaphore{ch: make(chan struct{}, capacity)}
}

// TryAcquire takes a slot without blocking. It returns false when full and
// increments the rejection counter.
func (s *Semaphore) TryAcquire() bool {
	select {
	case s.ch <- struct{}{}:
		return true
	default:
		s.rejected.Add(1)
		return false
	}
}

// Release returns a slot. It is safe to call more times than acquired.
func (s *Semaphore) Release() {
	select {
	case <-s.ch:
	default:
	}
}

// Len reports the number of occupied slots.
func (s *Semaphore) Len() int {
	return len(s.ch)
}

// Rejected reports how many acquisitions were refused because the gate was full.
func (s *Semaphore) Rejected() uint64 {
	return s.rejected.Load()
}

// AdmissionSnapshot is a point-in-time view of session admission state.
type AdmissionSnapshot struct {
	MaxTotalSessions     int            `json:"max_total_sessions"`
	MaxSessionsPerDevice int            `json:"max_sessions_per_device"`
	MaxPendingHandshakes int            `json:"max_pending_handshakes"`
	MaxWSClients         int            `json:"max_ws_clients"`
	TotalSessions        int            `json:"total_sessions"`
	PerDevice            map[string]int `json:"per_device"`
	RejectedGlobal       uint64         `json:"rejected_global"`
	RejectedPerDevice    uint64         `json:"rejected_per_device"`
	RejectedHandshake    uint64         `json:"rejected_handshake"`
	RejectedWS           uint64         `json:"rejected_ws"`
}

// AdmissionController enforces global and per-device session caps. The
// controller is shared by Mode 1 and Mode 2 so the global cap is genuinely
// process-wide.
type AdmissionController struct {
	mu        sync.Mutex
	limits    config.ConnectionLimitsConfig
	total     int
	perDevice map[string]int

	rejectedGlobal    atomic.Uint64
	rejectedPerDevice atomic.Uint64
	rejectedHandshake atomic.Uint64
	rejectedWS        atomic.Uint64
}

// NewAdmissionController creates a controller from the configured limits.
func NewAdmissionController(limits config.ConnectionLimitsConfig) *AdmissionController {
	return &AdmissionController{
		limits:    limits,
		perDevice: make(map[string]int),
	}
}

// TryAcquire reserves one session slot for deviceID. The returned release
// function must be called exactly once when the session ends.
func (a *AdmissionController) TryAcquire(deviceID string) (release func(), ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.limits.MaxTotalSessions > 0 && a.total >= a.limits.MaxTotalSessions {
		a.rejectedGlobal.Add(1)
		return nil, false
	}
	if a.limits.MaxSessionsPerDevice > 0 && a.perDevice[deviceID] >= a.limits.MaxSessionsPerDevice {
		a.rejectedPerDevice.Add(1)
		return nil, false
	}

	a.total++
	a.perDevice[deviceID]++

	released := false
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if released {
			return
		}
		released = true
		if a.total > 0 {
			a.total--
		}
		a.perDevice[deviceID]--
		if a.perDevice[deviceID] <= 0 {
			delete(a.perDevice, deviceID)
		}
	}, true
}

// RecordHandshakeRejection increments the pending-handshake overload counter.
func (a *AdmissionController) RecordHandshakeRejection() {
	a.rejectedHandshake.Add(1)
}

// RecordWSRejection increments the WebSocket overload counter.
func (a *AdmissionController) RecordWSRejection() {
	a.rejectedWS.Add(1)
}

// Snapshot returns a copy of the current admission state.
func (a *AdmissionController) Snapshot() AdmissionSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()

	perDevice := make(map[string]int, len(a.perDevice))
	for id, count := range a.perDevice {
		perDevice[id] = count
	}

	return AdmissionSnapshot{
		MaxTotalSessions:     a.limits.MaxTotalSessions,
		MaxSessionsPerDevice: a.limits.MaxSessionsPerDevice,
		MaxPendingHandshakes: a.limits.MaxPendingHandshakes,
		MaxWSClients:         a.limits.MaxWSClients,
		TotalSessions:        a.total,
		PerDevice:            perDevice,
		RejectedGlobal:       a.rejectedGlobal.Load(),
		RejectedPerDevice:    a.rejectedPerDevice.Load(),
		RejectedHandshake:    a.rejectedHandshake.Load(),
		RejectedWS:           a.rejectedWS.Load(),
	}
}

// RejectConn closes a TCP connection with SO_LINGER=0 so overload rejection is
// visible as a reset instead of a half-open socket.
func RejectConn(conn net.Conn) {
	if conn == nil {
		return
	}
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.SetLinger(0)
	}
	_ = conn.Close()
}
