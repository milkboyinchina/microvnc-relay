package proxy

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/milkboyinchina/microvnc-relay/internal/telemetry"
)

const (
	// HistorySlots24h stores 1,440 1-minute buckets (24 hours of traffic history in ~46 KB RAM).
	HistorySlots24h = 1440
)

// BandwidthBucket represents 1 minute of aggregated RX/TX byte throughput.
type BandwidthBucket struct {
	Timestamp int64  `json:"timestamp"`
	RxBytes   uint64 `json:"rx_bytes"`
	TxBytes   uint64 `json:"tx_bytes"`
}

// SessionSnapshot represents a point-in-time view of an active VNC proxy stream.
type SessionSnapshot struct {
	SessionID       string  `json:"session_id"`
	DeviceID        string  `json:"device_id"`
	DeviceName      string  `json:"device_name"`
	Mode            string  `json:"mode"`
	ClientIP        string  `json:"client_ip"`
	ClientRTTMs     float64 `json:"client_rtt_ms"`
	TargetIP        string  `json:"target_ip"`
	TargetRTTMs     float64 `json:"target_rtt_ms"`
	RxBytesTotal    uint64  `json:"rx_bytes_total"`
	TxBytesTotal    uint64  `json:"tx_bytes_total"`
	RxBytesSec      uint64  `json:"rx_bytes_sec"`
	TxBytesSec      uint64  `json:"tx_bytes_sec"`
	DurationSeconds int64   `json:"duration_seconds"`
}

// TrafficSnapshot represents global traffic throughput and cumulative byte totals.
type TrafficSnapshot struct {
	TotalRxBytesSec   uint64            `json:"total_rx_bytes_sec"`
	TotalTxBytesSec   uint64            `json:"total_tx_bytes_sec"`
	TotalCumulativeRx uint64            `json:"total_cumulative_rx"`
	TotalCumulativeTx uint64            `json:"total_cumulative_tx"`
	History24h        []BandwidthBucket `json:"history_24h,omitempty"`
}

// Session tracks real-time metrics and lifecycle for a single active VNC connection.
type Session struct {
	ID              string
	DeviceID        string
	DeviceName      string
	Mode            string
	ClientAddr      string
	TargetAddr      string
	StartTime       time.Time
	meter           *TrafficMeter
	rxBytes         atomic.Uint64
	txBytes         atomic.Uint64
	lastRxBytes     uint64
	lastTxBytes     uint64
	rxBytesSec      atomic.Uint64
	txBytesSec      atomic.Uint64
	clientRTTMicros atomic.Int64
	targetRTTMicros atomic.Int64
	connMu          sync.RWMutex
	clientConn      net.Conn
	disconnectOnce  sync.Once
	disconnectFn    func()
}

// SetClientConn associates the client TCP connection with the session for Hop 1 TCP_INFO inspection.
func (s *Session) SetClientConn(conn net.Conn) {
	s.connMu.Lock()
	s.clientConn = conn
	s.connMu.Unlock()
}

// ClientConn returns the underlying client connection for Hop 1 TCP_INFO inspection.
func (s *Session) ClientConn() net.Conn {
	s.connMu.RLock()
	defer s.connMu.RUnlock()
	return s.clientConn
}

// AddRx atomically increments viewer-to-target byte counters.
func (s *Session) AddRx(n uint64) {
	if n == 0 {
		return
	}
	s.rxBytes.Add(n)
	if s.meter != nil && s.meter.enabled.Load() {
		s.meter.totalRx.Add(n)
	}
}

// AddTx atomically increments target-to-viewer byte counters.
func (s *Session) AddTx(n uint64) {
	if n == 0 {
		return
	}
	s.txBytes.Add(n)
	if s.meter != nil && s.meter.enabled.Load() {
		s.meter.totalTx.Add(n)
	}
}

// SetClientRTT updates the measured Hop 1 (Client <-> Relay) round-trip time.
func (s *Session) SetClientRTT(d time.Duration) {
	s.clientRTTMicros.Store(d.Microseconds())
}

// SetTargetRTT updates the measured Hop 2 (Relay <-> Target) round-trip time.
func (s *Session) SetTargetRTT(d time.Duration) {
	s.targetRTTMicros.Store(d.Microseconds())
}

// Disconnect invokes the session teardown callback once.
func (s *Session) Disconnect() {
	s.disconnectOnce.Do(func() {
		if s.disconnectFn != nil {
			s.disconnectFn()
		}
	})
}

// Snapshot returns a serializable view of the session's current state.
func (s *Session) Snapshot(now time.Time) SessionSnapshot {
	dur := int64(now.Sub(s.StartTime).Seconds())
	if dur < 0 {
		dur = 0
	}
	return SessionSnapshot{
		SessionID:       s.ID,
		DeviceID:        s.DeviceID,
		DeviceName:      s.DeviceName,
		Mode:            s.Mode,
		ClientIP:        s.ClientAddr,
		ClientRTTMs:     float64(s.clientRTTMicros.Load()) / 1000.0,
		TargetIP:        s.TargetAddr,
		TargetRTTMs:     float64(s.targetRTTMicros.Load()) / 1000.0,
		RxBytesTotal:    s.rxBytes.Load(),
		TxBytesTotal:    s.txBytes.Load(),
		RxBytesSec:      s.rxBytesSec.Load(),
		TxBytesSec:      s.txBytesSec.Load(),
		DurationSeconds: dur,
	}
}

// MeteredConn wraps a net.Conn and atomically counts read and written bytes.
type MeteredConn struct {
	net.Conn
	onRead  func(uint64)
	onWrite func(uint64)
}

// NewMeteredConn wraps conn with atomic read/write callbacks.
func NewMeteredConn(conn net.Conn, onRead, onWrite func(uint64)) *MeteredConn {
	return &MeteredConn{
		Conn:    conn,
		onRead:  onRead,
		onWrite: onWrite,
	}
}

// Read reads from the underlying connection and increments the read counter.
func (m *MeteredConn) Read(b []byte) (int, error) {
	n, err := m.Conn.Read(b)
	if n > 0 && m.onRead != nil {
		m.onRead(uint64(n))
	}
	return n, err
}

// Write writes to the underlying connection and increments the write counter.
func (m *MeteredConn) Write(b []byte) (int, error) {
	n, err := m.Conn.Write(b)
	if n > 0 && m.onWrite != nil {
		m.onWrite(uint64(n))
	}
	return n, err
}

// UnderlyingConn returns the wrapped net.Conn for socket option or TCP_INFO inspection.
func (m *MeteredConn) UnderlyingConn() net.Conn {
	return m.Conn
}

// TrafficMeter coordinates active sessions, 1Hz throughput sampling, and the 24h ring buffer.
type TrafficMeter struct {
	enabled       atomic.Bool
	seq           atomic.Uint64
	totalRx       atomic.Uint64
	totalTx       atomic.Uint64
	totalRxSec    atomic.Uint64
	totalTxSec    atomic.Uint64
	lastTotalRx   uint64
	lastTotalTx   uint64
	minuteRxAccum uint64
	minuteTxAccum uint64
	tickCount     int
	history24h    *telemetry.RingBuffer[BandwidthBucket]
	mu            sync.RWMutex
	sessions      map[string]*Session
}

// NewTrafficMeter initializes a TrafficMeter with a fixed 1,440-slot 24h ring buffer.
func NewTrafficMeter(enabled bool) *TrafficMeter {
	tm := &TrafficMeter{
		history24h: telemetry.NewRingBuffer[BandwidthBucket](HistorySlots24h),
		sessions:   make(map[string]*Session),
	}
	tm.enabled.Store(enabled)
	return tm
}

// SetEnabled toggles live byte counting at runtime.
func (tm *TrafficMeter) SetEnabled(enabled bool) {
	tm.enabled.Store(enabled)
}

// RegisterSession creates and registers a new active proxy session.
func (tm *TrafficMeter) RegisterSession(deviceID, deviceName, mode, clientAddr, targetAddr string, disconnectFn func()) *Session {
	id := fmt.Sprintf("sess-%d", tm.seq.Add(1))
	sess := &Session{
		ID:           id,
		DeviceID:     deviceID,
		DeviceName:   deviceName,
		Mode:         mode,
		ClientAddr:   clientAddr,
		TargetAddr:   targetAddr,
		StartTime:    time.Now(),
		meter:        tm,
		disconnectFn: disconnectFn,
	}

	tm.mu.Lock()
	tm.sessions[id] = sess
	tm.mu.Unlock()

	return sess
}

// UnregisterSession removes a completed session from the active registry.
func (tm *TrafficMeter) UnregisterSession(id string) {
	tm.mu.Lock()
	delete(tm.sessions, id)
	tm.mu.Unlock()
}

// DisconnectSession finds an active session by ID and invokes its disconnect handler.
func (tm *TrafficMeter) DisconnectSession(id string) bool {
	tm.mu.RLock()
	sess, ok := tm.sessions[id]
	tm.mu.RUnlock()
	if !ok {
		return false
	}
	sess.Disconnect()
	return true
}

// GetSessions returns a snapshot of all currently active sessions.
func (tm *TrafficMeter) GetSessions() []SessionSnapshot {
	now := time.Now()
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	out := make([]SessionSnapshot, 0, len(tm.sessions))
	for _, s := range tm.sessions {
		out = append(out, s.Snapshot(now))
	}
	return out
}

// ActiveSessionCountForDevice returns the number of active sessions routed to a given device ID.
func (tm *TrafficMeter) ActiveSessionCountForDevice(deviceID string) int {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	count := 0
	for _, s := range tm.sessions {
		if s.DeviceID == deviceID {
			count++
		}
	}
	return count
}

// ForEachSession invokes fn for each currently active session under a read lock.
func (tm *TrafficMeter) ForEachSession(fn func(*Session)) {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	for _, s := range tm.sessions {
		fn(s)
	}
}

// UpdateDeviceTargetRTT updates the Hop 2 TargetRTT on all active sessions routed to deviceID.
func (tm *TrafficMeter) UpdateDeviceTargetRTT(deviceID string, rtt time.Duration) {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	for _, s := range tm.sessions {
		if s.DeviceID == deviceID {
			s.SetTargetRTT(rtt)
		}
	}
}

// SampleTick should be called once per second (1Hz) to update instantaneous bytes/sec,
// sample Hop 1 kernel TCP_INFO RTT for active sessions, and record 1-minute buckets into the 24-hour ring buffer.
func (tm *TrafficMeter) SampleTick() {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	currRx := tm.totalRx.Load()
	currTx := tm.totalTx.Load()

	var deltaRx, deltaTx uint64
	if currRx >= tm.lastTotalRx {
		deltaRx = currRx - tm.lastTotalRx
	}
	if currTx >= tm.lastTotalTx {
		deltaTx = currTx - tm.lastTotalTx
	}
	tm.lastTotalRx = currRx
	tm.lastTotalTx = currTx

	tm.totalRxSec.Store(deltaRx)
	tm.totalTxSec.Store(deltaTx)

	for _, s := range tm.sessions {
		sRx := s.rxBytes.Load()
		sTx := s.txBytes.Load()
		var sDeltaRx, sDeltaTx uint64
		if sRx >= s.lastRxBytes {
			sDeltaRx = sRx - s.lastRxBytes
		}
		if sTx >= s.lastTxBytes {
			sDeltaTx = sTx - s.lastTxBytes
		}
		s.lastRxBytes = sRx
		s.lastTxBytes = sTx
		s.rxBytesSec.Store(sDeltaRx)
		s.txBytesSec.Store(sDeltaTx)

		if conn := s.ClientConn(); conn != nil {
			if rtt, err := telemetry.ExtractTCPInfoRTT(conn); err == nil && rtt > 0 {
				s.SetClientRTT(rtt)
			}
		}
	}

	tm.minuteRxAccum += deltaRx
	tm.minuteTxAccum += deltaTx
	tm.tickCount++

	if tm.tickCount >= 60 {
		tm.history24h.Push(BandwidthBucket{
			Timestamp: time.Now().Unix(),
			RxBytes:   tm.minuteRxAccum,
			TxBytes:   tm.minuteTxAccum,
		})
		tm.minuteRxAccum = 0
		tm.minuteTxAccum = 0
		tm.tickCount = 0
	}
}

// Snapshot returns current global traffic metrics and optionally the 24-hour bucket history.
func (tm *TrafficMeter) Snapshot(includeHistory bool) TrafficSnapshot {
	snap := TrafficSnapshot{
		TotalRxBytesSec:   tm.totalRxSec.Load(),
		TotalTxBytesSec:   tm.totalTxSec.Load(),
		TotalCumulativeRx: tm.totalRx.Load(),
		TotalCumulativeTx: tm.totalTx.Load(),
	}
	if includeHistory {
		snap.History24h = tm.history24h.Snapshot()
	}
	return snap
}

// ResetCounters zeroes all cumulative counters, active session counters, and the 24h ring buffer.
func (tm *TrafficMeter) ResetCounters() {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	tm.totalRx.Store(0)
	tm.totalTx.Store(0)
	tm.totalRxSec.Store(0)
	tm.totalTxSec.Store(0)
	tm.lastTotalRx = 0
	tm.lastTotalTx = 0
	tm.minuteRxAccum = 0
	tm.minuteTxAccum = 0
	tm.tickCount = 0
	tm.history24h.Reset()

	for _, s := range tm.sessions {
		s.rxBytes.Store(0)
		s.txBytes.Store(0)
		s.lastRxBytes = 0
		s.lastTxBytes = 0
		s.rxBytesSec.Store(0)
		s.txBytesSec.Store(0)
	}
}
