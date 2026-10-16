package telemetry

import (
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/milkboyinchina/microvnc-relay/internal/config"
)

// DeviceProbeStatus holds the latest Hop 2 (Relay <-> Target) reachability and RTT measurement.
type DeviceProbeStatus struct {
	DeviceID    string    `json:"device_id"`
	Online      bool      `json:"online"`
	TargetRTTMs float64   `json:"target_rtt_ms"`
	LastChecked time.Time `json:"last_checked"`
	LastError   string    `json:"last_error,omitempty"`
}

// ProbeTCPTarget performs a non-intrusive TCP handshake (SYN -> SYN/ACK) to host:port
// and immediately closes the socket, returning the measured round-trip duration.
func ProbeTCPTarget(host string, port int, timeout time.Duration) (time.Duration, error) {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, timeout)
	elapsed := time.Since(start)
	if err != nil {
		return 0, err
	}
	_ = conn.Close()
	return elapsed, nil
}

// Pinger executes periodic and on-demand Hop 2 TCP SYN latency probes against registered VNC targets.
type Pinger struct {
	cfg             *config.Config
	log             *slog.Logger
	onTargetRTT     func(deviceID string, rtt time.Duration)
	hasMode2Pending func(mode2ID int) bool

	mu       sync.RWMutex
	statuses map[string]DeviceProbeStatus
	stopCh   chan struct{}
	wg       sync.WaitGroup
	started  bool
	stopped  bool
}

// NewPinger creates a new Hop 2 TCP SYN latency engine.
func NewPinger(
	cfg *config.Config,
	log *slog.Logger,
	onTargetRTT func(deviceID string, rtt time.Duration),
	hasMode2Pending func(mode2ID int) bool,
) *Pinger {
	if log == nil {
		log = slog.Default()
	}
	return &Pinger{
		cfg:             cfg,
		log:             log,
		onTargetRTT:     onTargetRTT,
		hasMode2Pending: hasMode2Pending,
		statuses:        make(map[string]DeviceProbeStatus),
		stopCh:          make(chan struct{}),
	}
}

// Start launches the background TCP SYN polling ticker if features.latency_probes_enabled is true.
func (p *Pinger) Start() {
	p.mu.Lock()
	if p.started || p.stopped {
		p.mu.Unlock()
		return
	}
	p.started = true
	p.mu.Unlock()

	if !p.cfg.Features.LatencyProbesEnabled {
		p.log.Info("Hop 2 background latency probes disabled by feature flag")
		return
	}

	intervalSec := p.cfg.Telemetry.TargetPingIntervalSec
	if intervalSec <= 0 {
		intervalSec = 15
	}
	interval := time.Duration(intervalSec) * time.Second

	p.log.Info("started Hop 2 TCP SYN latency pinger", "interval_sec", intervalSec)

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.ProbeAll()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				p.ProbeAll()
			case <-p.stopCh:
				return
			}
		}
	}()
}

// Stop terminates the background latency pinger and waits for any active sweep to finish.
func (p *Pinger) Stop() {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return
	}
	p.stopped = true
	close(p.stopCh)
	wasStarted := p.started
	p.mu.Unlock()

	if wasStarted {
		p.wg.Wait()
	}
}

// ProbeAll sweeps all configured devices and prunes stale entries from the status map.
func (p *Pinger) ProbeAll() {
	devices := p.cfg.GetDevices()
	activeIDs := make(map[string]struct{}, len(devices))

	for _, dev := range devices {
		activeIDs[dev.ID] = struct{}{}
		select {
		case <-p.stopCh:
			return
		default:
		}
		if !dev.Enabled {
			p.mu.Lock()
			p.statuses[dev.ID] = DeviceProbeStatus{
				DeviceID:    dev.ID,
				Online:      false,
				TargetRTTMs: 0,
				LastChecked: time.Now(),
			}
			p.mu.Unlock()
			continue
		}
		p.ProbeDevice(dev)
	}

	// Prune removed devices so map size stays strictly bounded to len(devices)
	p.mu.Lock()
	for id := range p.statuses {
		if _, ok := activeIDs[id]; !ok {
			delete(p.statuses, id)
		}
	}
	p.mu.Unlock()
}

// ProbeDevice executes an immediate TCP SYN probe for a single device and updates its cached status.
func (p *Pinger) ProbeDevice(dev config.DeviceConfig) DeviceProbeStatus {
	timeout := time.Duration(p.cfg.ConnectionLimits.DialTimeoutSec) * time.Second
	if timeout <= 0 || timeout > 5*time.Second {
		timeout = 3 * time.Second
	}

	status := DeviceProbeStatus{
		DeviceID:    dev.ID,
		LastChecked: time.Now(),
	}

	// If this is a Mode 2 device and a reverse server is currently waiting in the Mode 2 pool,
	// mark it online even if the target is behind NAT and not directly dialable.
	if dev.Mode2ID > 0 && p.hasMode2Pending != nil && p.hasMode2Pending(dev.Mode2ID) {
		status.Online = true
	}

	if dev.Host != "" && dev.Port > 0 {
		rtt, err := ProbeTCPTarget(dev.Host, dev.Port, timeout)
		if err == nil {
			status.Online = true
			status.TargetRTTMs = float64(rtt.Microseconds()) / 1000.0
			status.LastError = ""
			if p.onTargetRTT != nil {
				p.onTargetRTT(dev.ID, rtt)
			}
		} else if !status.Online {
			status.LastError = err.Error()
		}
	}

	p.mu.Lock()
	p.statuses[dev.ID] = status
	p.mu.Unlock()

	return status
}

// GetStatus returns the cached probe status for a single device ID.
func (p *Pinger) GetStatus(deviceID string) (DeviceProbeStatus, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	st, ok := p.statuses[deviceID]
	return st, ok
}

// GetAllStatuses returns a snapshot copy of all device probe statuses.
func (p *Pinger) GetAllStatuses() map[string]DeviceProbeStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]DeviceProbeStatus, len(p.statuses))
	for k, v := range p.statuses {
		out[k] = v
	}
	return out
}
