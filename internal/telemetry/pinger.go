package telemetry

import (
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/milkboyinchina/microvnc-relay/internal/config"
)

const (
	// maxConcurrentProbes bounds every probe source (background scheduler,
	// manual sweep, and the on-demand /test endpoint) to four sockets.
	maxConcurrentProbes = 4
	// schedulerTick is the coarse wakeup interval for per-host due checks.
	schedulerTick = time.Second
)

var (
	// ErrProbeBusy is returned when all probe slots are occupied. Callers
	// should return 429/503 rather than queueing an unbounded goroutine.
	ErrProbeBusy = errors.New("probe scheduler is busy")
	// ErrProbeStopped is returned when a probe is requested during shutdown.
	ErrProbeStopped = errors.New("probe scheduler is stopped")
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
//
// The socket is closed with SO_LINGER=0 so repeated probes do not accumulate
// TIME_WAIT entries on constrained boards.
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
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.SetLinger(0)
	}
	_ = conn.Close()
	return elapsed, nil
}

// Pinger executes periodic and on-demand Hop 2 TCP SYN latency probes against
// registered targets through one shared, bounded scheduler.
type Pinger struct {
	cfg             *config.Config
	log             *slog.Logger
	onTargetRTT     func(deviceID string, rtt time.Duration)
	hasMode2Pending func(mode2ID int) bool

	mu        sync.RWMutex
	statuses  map[string]DeviceProbeStatus
	nextProbe map[string]time.Time

	stopCh  chan struct{}
	wg      sync.WaitGroup // scheduler loop
	probeWG sync.WaitGroup // individual probes and sweep goroutines

	probeMu      sync.Mutex
	probesStopped bool
	probeSem     chan struct{}
	sweepRunning atomic.Bool

	started bool
	stopped bool

	intervalMu sync.RWMutex
	interval   time.Duration

	nowFn    func() time.Time
	jitterFn func(time.Duration) time.Duration
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

	intervalSec := cfg.Telemetry.TargetPingIntervalSec
	if intervalSec <= 0 {
		intervalSec = 15
	}

	return &Pinger{
		cfg:             cfg,
		log:             log,
		onTargetRTT:     onTargetRTT,
		hasMode2Pending: hasMode2Pending,
		statuses:        make(map[string]DeviceProbeStatus),
		nextProbe:       make(map[string]time.Time),
		stopCh:          make(chan struct{}),
		probeSem:        make(chan struct{}, maxConcurrentProbes),
		interval:        time.Duration(intervalSec) * time.Second,
		nowFn:           time.Now,
		jitterFn:        defaultJitter,
	}
}

// SetInterval updates the background probe cadence. It is safe to call before
// or after Start; the scheduler reads the value on the next due calculation.
func (p *Pinger) SetInterval(sec int) {
	if sec <= 0 {
		sec = 15
	}
	p.intervalMu.Lock()
	p.interval = time.Duration(sec) * time.Second
	p.intervalMu.Unlock()
}

func (p *Pinger) currentInterval() time.Duration {
	p.intervalMu.RLock()
	defer p.intervalMu.RUnlock()
	if p.interval <= 0 {
		return 15 * time.Second
	}
	return p.interval
}

func defaultJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 15 * time.Second
	}
	// +/-20% spreads probes from many relays so a shared target does not see a
	// synchronized burst every interval.
	factor := 0.8 + rand.Float64()*0.4
	return time.Duration(float64(d) * factor)
}

func randomInitialOffset(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(rand.Float64() * float64(d))
}

// Start launches the bounded background probe scheduler if
// features.latency_probes_enabled is true.
func (p *Pinger) Start() {
	p.mu.Lock()
	if p.started || p.stopped {
		p.mu.Unlock()
		return
	}
	p.started = true
	if !p.cfg.Features.LatencyProbesEnabled {
		p.mu.Unlock()
		p.log.Info("Hop 2 background latency probes disabled by feature flag")
		return
	}
	now := p.nowFn()
	interval := p.currentInterval()
	for _, dev := range p.cfg.GetDevices() {
		if dev.Enabled {
			p.nextProbe[dev.ID] = now.Add(randomInitialOffset(interval))
		}
	}
	p.mu.Unlock()

	p.log.Info("started Hop 2 TCP SYN latency pinger",
		"interval", p.currentInterval().String(),
		"max_concurrent", maxConcurrentProbes,
	)

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()

		ticker := time.NewTicker(schedulerTick)
		defer ticker.Stop()

		p.schedulerTick()
		for {
			select {
			case <-ticker.C:
				p.schedulerTick()
			case <-p.stopCh:
				return
			}
		}
	}()
}

// Stop terminates the scheduler and waits for every in-flight probe.
func (p *Pinger) Stop() {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return
	}
	p.stopped = true
	wasStarted := p.started
	close(p.stopCh)
	p.mu.Unlock()

	// Prevent new probe WaitGroup additions before waiting.
	p.probeMu.Lock()
	p.probesStopped = true
	p.probeMu.Unlock()

	if wasStarted {
		p.wg.Wait()
	}
	p.probeWG.Wait()
}

func (p *Pinger) beginProbe() bool {
	p.probeMu.Lock()
	defer p.probeMu.Unlock()
	if p.probesStopped {
		return false
	}
	p.probeWG.Add(1)
	return true
}

func (p *Pinger) endProbe() {
	p.probeWG.Done()
}

func (p *Pinger) schedulerTick() {
	now := p.nowFn()
	devices := p.cfg.GetDevices()
	active := make(map[string]struct{}, len(devices))

	for _, dev := range devices {
		active[dev.ID] = struct{}{}
		if !dev.Enabled {
			continue
		}

		p.mu.RLock()
		due, ok := p.nextProbe[dev.ID]
		p.mu.RUnlock()
		if !ok {
			due = now
		}
		if now.Before(due) {
			continue
		}
		p.scheduleProbe(dev, now)
	}

	p.pruneSchedule(active)
}

func (p *Pinger) scheduleProbe(dev config.DeviceConfig, now time.Time) {
	select {
	case p.probeSem <- struct{}{}:
	default:
		// All four slots are busy. Leave nextProbe unchanged so the next
		// scheduler tick retries this host.
		return
	}

	if !p.beginProbe() {
		<-p.probeSem
		return
	}

	p.mu.Lock()
	p.nextProbe[dev.ID] = now.Add(p.jitterFn(p.currentInterval()))
	p.mu.Unlock()

	go func() {
		defer p.endProbe()
		defer func() { <-p.probeSem }()
		p.probeDevice(dev)
	}()
}

// TriggerSweep starts one asynchronous full sweep if none is already running.
// This is the only async probe entry point used by API/reload paths. It is a
// no-op when the pinger has not been started or background probes are disabled;
// the synchronous /test path remains available either way.
func (p *Pinger) TriggerSweep() {
	p.mu.RLock()
	started := p.started
	stopped := p.stopped
	p.mu.RUnlock()
	if !started || stopped || !p.cfg.Features.LatencyProbesEnabled {
		return
	}

	if !p.sweepRunning.CompareAndSwap(false, true) {
		return
	}
	if !p.beginProbe() {
		p.sweepRunning.Store(false)
		return
	}

	go func() {
		defer p.endProbe()
		defer p.sweepRunning.Store(false)
		p.ProbeAll()
	}()
}

// ProbeAll performs a synchronous sweep of all configured devices through the
// shared four-slot semaphore. It is used by tests and by TriggerSweep.
func (p *Pinger) ProbeAll() {
	devices := p.cfg.GetDevices()
	now := p.nowFn()
	activeIDs := make(map[string]struct{}, len(devices))

	var wg sync.WaitGroup
	for _, dev := range devices {
		activeIDs[dev.ID] = struct{}{}

		if !dev.Enabled {
			p.mu.Lock()
			p.statuses[dev.ID] = DeviceProbeStatus{
				DeviceID:    dev.ID,
				Online:      false,
				TargetRTTMs: 0,
				LastChecked: now,
			}
			p.mu.Unlock()
			continue
		}

		select {
		case p.probeSem <- struct{}{}:
		case <-p.stopCh:
			wg.Wait()
			return
		}

		if !p.beginProbe() {
			<-p.probeSem
			wg.Wait()
			return
		}

		p.mu.Lock()
		p.nextProbe[dev.ID] = now.Add(p.jitterFn(p.currentInterval()))
		p.mu.Unlock()

		wg.Add(1)
		go func(d config.DeviceConfig) {
			defer wg.Done()
			defer p.endProbe()
			defer func() { <-p.probeSem }()
			p.probeDevice(d)
		}(dev)
	}

	wg.Wait()

	// Prune removed devices so map sizes stay strictly bounded.
	p.mu.Lock()
	for id := range p.statuses {
		if _, ok := activeIDs[id]; !ok {
			delete(p.statuses, id)
		}
	}
	p.mu.Unlock()
	p.pruneSchedule(activeIDs)
}

func (p *Pinger) pruneSchedule(active map[string]struct{}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id := range p.nextProbe {
		if _, ok := active[id]; !ok {
			delete(p.nextProbe, id)
		}
	}
}

// ProbeDeviceNow runs an immediate bounded probe for one device. It never
// spawns a goroutine; callers receive ErrProbeBusy when all slots are taken.
func (p *Pinger) ProbeDeviceNow(dev config.DeviceConfig) (DeviceProbeStatus, error) {
	select {
	case p.probeSem <- struct{}{}:
	default:
		return DeviceProbeStatus{}, ErrProbeBusy
	}
	defer func() { <-p.probeSem }()

	if !p.beginProbe() {
		return DeviceProbeStatus{}, ErrProbeStopped
	}
	defer p.endProbe()

	return p.probeDevice(dev), nil
}

// ProbeDevice executes a synchronous probe for tests and legacy callers. All
// production probe sources use ProbeDeviceNow, TriggerSweep, or the scheduler.
func (p *Pinger) ProbeDevice(dev config.DeviceConfig) DeviceProbeStatus {
	select {
	case p.probeSem <- struct{}{}:
	case <-p.stopCh:
		return DeviceProbeStatus{DeviceID: dev.ID, LastChecked: p.nowFn()}
	}
	defer func() { <-p.probeSem }()

	return p.probeDevice(dev)
}

func (p *Pinger) probeDevice(dev config.DeviceConfig) DeviceProbeStatus {
	timeout := time.Duration(p.cfg.ConnectionLimits.DialTimeoutSec) * time.Second
	if timeout <= 0 || timeout > 5*time.Second {
		timeout = 3 * time.Second
	}

	status := DeviceProbeStatus{
		DeviceID:    dev.ID,
		LastChecked: p.nowFn(),
	}

	// If this is a Mode 2 device and a reverse server is currently waiting in
	// the Mode 2 pool, mark it online even if the target is behind NAT and not
	// directly dialable.
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
