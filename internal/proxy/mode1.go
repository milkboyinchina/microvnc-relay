package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/milkboyinchina/microvnc-relay/internal/config"
)

type mode1Listener struct {
	device   config.DeviceConfig
	listener net.Listener
	stopCh   chan struct{}
}

// Mode1Manager manages dedicated virtual port listeners for Mode 1 direct port-mapped devices.
type Mode1Manager struct {
	cfg          *config.Config
	meter        *TrafficMeter
	log          *slog.Logger
	mu           sync.Mutex
	listeners    map[string]*mode1Listener
	bindErrors   map[string]string
	acceptWG     sync.WaitGroup
	connWG       sync.WaitGroup
	stopped      bool
	admission    *AdmissionController
	handshakeSem *Semaphore
}

// NewMode1Manager creates a new Mode 1 Port-Mapped Proxy manager.
func NewMode1Manager(cfg *config.Config, meter *TrafficMeter, log *slog.Logger) *Mode1Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Mode1Manager{
		cfg:          cfg,
		meter:        meter,
		log:          log,
		listeners:    make(map[string]*mode1Listener),
		bindErrors:   make(map[string]string),
		handshakeSem: NewSemaphore(cfg.ConnectionLimits.MaxPendingHandshakes),
	}
}

// SetAdmissionController attaches the process-wide session admission controller.
// It must be called before Start.
func (m *Mode1Manager) SetAdmissionController(admission *AdmissionController) {
	m.admission = admission
}

// SetHandshakeSemaphore attaches a process-wide pending-handshake gate. Mode 1
// and Mode 2 share one semaphore so max_pending_handshakes bounds the whole
// process, not each listener group independently.
func (m *Mode1Manager) SetHandshakeSemaphore(sem *Semaphore) {
	if sem != nil {
		m.handshakeSem = sem
	}
}

// Start opens TCP listeners for all enabled Mode 1 devices in the configuration.
func (m *Mode1Manager) Start() error {
	if !m.cfg.Features.Mode1Enabled {
		m.log.Info("Mode 1 proxy disabled by feature flag")
		return nil
	}
	return m.Reload(m.cfg.GetDevices())
}

// Reload dynamically reconciles active TCP listeners with the provided device list.
// Unchanged listeners continue running uninterrupted; removed or modified listeners are closed and replaced.
func (m *Mode1Manager) Reload(devices []config.DeviceConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.stopped || !m.cfg.Features.Mode1Enabled {
		return nil
	}

	desired := make(map[string]config.DeviceConfig)
	for _, dev := range devices {
		if dev.Enabled && dev.VirtualPort > 0 {
			desired[dev.ID] = dev
		}
	}

	// Prune stale bind errors for removed or disabled devices
	for id := range m.bindErrors {
		if _, exists := desired[id]; !exists {
			delete(m.bindErrors, id)
		}
	}

	// Stop listeners that were removed or had their port/target changed
	for id, active := range m.listeners {
		want, exists := desired[id]
		if !exists ||
			active.device.VirtualPort != want.VirtualPort ||
			active.device.Host != want.Host ||
			active.device.Port != want.Port {
			close(active.stopCh)
			_ = active.listener.Close()
			delete(m.listeners, id)
			m.log.Info("stopped Mode 1 listener", "device_id", id, "virtual_port", active.device.VirtualPort)
		} else {
			// Update metadata such as Name
			active.device = want
		}
	}

	var firstErr error
	for id, dev := range desired {
		if _, alreadyRunning := m.listeners[id]; alreadyRunning {
			delete(m.bindErrors, id)
			continue
		}
		if err := m.startDeviceListenerLocked(dev); err != nil {
			m.bindErrors[id] = fmt.Sprintf("Port :%d already in use (%v)", dev.VirtualPort, err)
			m.log.Error("[PORT COLLISION] failed to bind Mode 1 virtual port",
				"device_id", id,
				"virtual_port", dev.VirtualPort,
				"err", err,
			)
			if firstErr == nil {
				firstErr = err
			}
		} else {
			delete(m.bindErrors, id)
		}
	}

	return firstErr
}

func (m *Mode1Manager) startDeviceListenerLocked(dev config.DeviceConfig) error {
	bindHost := m.cfg.Server.VNCBind
	if bindHost == "" {
		bindHost = "0.0.0.0"
	}
	bindAddr := net.JoinHostPort(bindHost, fmt.Sprintf("%d", dev.VirtualPort))
	ln, err := net.Listen("tcp", bindAddr)
	if err != nil {
		return fmt.Errorf("listen %s for device %s: %w", bindAddr, dev.ID, err)
	}

	m1 := &mode1Listener{
		device:   dev,
		listener: ln,
		stopCh:   make(chan struct{}),
	}
	m.listeners[dev.ID] = m1

	m.log.Info("started Mode 1 listener",
		"device_id", dev.ID,
		"name", dev.Name,
		"listen", ln.Addr().String(),
		"target", net.JoinHostPort(dev.Host, fmt.Sprintf("%d", dev.Port)),
	)

	m.acceptWG.Add(1)
	go m.acceptLoop(m1)
	return nil
}

func (m *Mode1Manager) acceptLoop(m1 *mode1Listener) {
	defer m.acceptWG.Done()

	for {
		clientConn, err := m1.listener.Accept()
		if err != nil {
			select {
			case <-m1.stopCh:
				return
			default:
				if errors.Is(err, net.ErrClosed) {
					return
				}
				m.log.Warn("Mode 1 accept error", "device_id", m1.device.ID, "err", err)
				continue
			}
		}

		m.mu.Lock()
		dev := m1.device
		m.mu.Unlock()

		m.connWG.Add(1)
		go func(conn net.Conn, d config.DeviceConfig) {
			defer m.connWG.Done()
			m.handleClient(conn, d)
		}(clientConn, dev)
	}
}

func (m *Mode1Manager) handleClient(clientConn net.Conn, dev config.DeviceConfig) {
	ConfigureTCPConn(clientConn, m.cfg.ConnectionLimits.TCPKeepAliveSec, m.cfg.ConnectionLimits.TCPUserTimeoutSec)

	handshakeHeld := false
	if m.handshakeSem != nil {
		if !m.handshakeSem.TryAcquire() {
			if m.admission != nil {
				m.admission.RecordHandshakeRejection()
			}
			m.log.Warn("Mode 1 rejected: pending handshake cap reached",
				"device_id", dev.ID,
				"client_ip", clientConn.RemoteAddr().String(),
			)
			RejectConn(clientConn)
			return
		}
		handshakeHeld = true
	}
	releaseHandshake := func() {
		if handshakeHeld {
			m.handshakeSem.Release()
			handshakeHeld = false
		}
	}
	defer releaseHandshake()

	remoteIP := ExtractRemoteIP(clientConn.RemoteAddr())
	if !m.cfg.IsIPAllowed(remoteIP) {
		m.log.Warn("rejected Mode 1 client outside CIDR allowlist",
			"device_id", dev.ID,
			"client_ip", clientConn.RemoteAddr().String(),
		)
		_ = clientConn.Close()
		return
	}

	if m.admission != nil {
		releaseAdmission, ok := m.admission.TryAcquire(dev.ID)
		if !ok {
			m.log.Warn("Mode 1 rejected: session admission cap reached",
				"device_id", dev.ID,
				"client_ip", clientConn.RemoteAddr().String(),
			)
			RejectConn(clientConn)
			return
		}
		defer releaseAdmission()
	}

	if rst, drop := faultActive(); rst {
		m.log.Warn("Mode 1 fault injection: resetting client", "device_id", dev.ID)
		RejectConn(clientConn)
		return
	} else if drop {
		m.log.Warn("Mode 1 fault injection: dropping client", "device_id", dev.ID)
		_ = clientConn.Close()
		return
	}

	targetAddr := net.JoinHostPort(dev.Host, fmt.Sprintf("%d", dev.Port))
	dialTimeout := time.Duration(m.cfg.ConnectionLimits.DialTimeoutSec) * time.Second
	if dialTimeout <= 0 {
		dialTimeout = 5 * time.Second
	}

	dialStart := time.Now()
	targetConn, err := net.DialTimeout("tcp", targetAddr, dialTimeout)
	dialRTT := time.Since(dialStart)
	if err != nil {
		m.log.Warn("Mode 1 target dial failed",
			"device_id", dev.ID,
			"target", targetAddr,
			"err", err,
		)
		_ = clientConn.Close()
		return
	}
	ConfigureTCPConn(targetConn, m.cfg.ConnectionLimits.TCPKeepAliveSec, m.cfg.ConnectionLimits.TCPUserTimeoutSec)

	closeBoth := func() {
		_ = clientConn.Close()
		_ = targetConn.Close()
	}

	var sess *Session
	if m.meter != nil {
		sess = m.meter.RegisterSession(
			dev.ID,
			dev.Name,
			"mode1",
			clientConn.RemoteAddr().String(),
			targetAddr,
			closeBoth,
		)
		if sess == nil {
			m.log.Warn("Mode 1 session rejected during shutdown", "device_id", dev.ID)
			closeBoth()
			return
		}
		sess.SetTargetRTT(dialRTT)
		defer m.meter.UnregisterSession(sess.ID)
	}

	// The socket is now authenticated/admitted; release the pre-auth slot.
	releaseHandshake()

	m.log.Info("Mode 1 session established",
		"session_id", func() string {
			if sess != nil {
				return sess.ID
			}
			return "unmetered"
		}(),
		"device_id", dev.ID,
		"client", clientConn.RemoteAddr().String(),
		"target", targetAddr,
		"dial_rtt_ms", float64(dialRTT.Microseconds())/1000.0,
	)

	ForwardStream(
		clientConn,
		targetConn,
		sess,
		m.cfg.ConnectionLimits,
		m.cfg.Features.BandwidthMeteringEnabled,
	)

	m.log.Info("Mode 1 session closed",
		"device_id", dev.ID,
		"client", clientConn.RemoteAddr().String(),
	)
}

// ListenerAddr returns the bound local address for a given device ID, or empty string if not listening.
func (m *Mode1Manager) ListenerAddr(deviceID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m1, ok := m.listeners[deviceID]; ok {
		return m1.listener.Addr().String()
	}
	return ""
}

// GetBindErrors returns a thread-safe snapshot of per-device port collision/bind errors.
func (m *Mode1Manager) GetBindErrors() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]string, len(m.bindErrors))
	for k, v := range m.bindErrors {
		out[k] = v
	}
	return out
}

// StopAccepting closes all Mode 1 listeners and prevents new sessions. Active
// sessions are closed by TrafficMeter.Shutdown.
func (m *Mode1Manager) StopAccepting() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.stopped {
		return
	}
	m.stopped = true
	for id, m1 := range m.listeners {
		close(m1.stopCh)
		_ = m1.listener.Close()
		delete(m.listeners, id)
	}
	m.bindErrors = make(map[string]string)
}

// Stop closes listeners and waits for accept loops and active handlers. The
// wait is bounded by ctx; a timed-out shutdown reports the remaining handlers.
func (m *Mode1Manager) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.StopAccepting()
	m.acceptWG.Wait()

	done := make(chan struct{})
	go func() {
		m.connWG.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("mode1 shutdown timed out with active handlers: %w", ctx.Err())
	}
}
