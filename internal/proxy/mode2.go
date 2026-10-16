package proxy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/milkboyinchina/microvnc-relay/internal/config"
)

const (
	// mode2PacketLen is the standard 250-byte handshake buffer size used by UltraVNC Mode II and x11vnc.
	mode2PacketLen = 250
	// mode2ViewerGreeting is the 12-byte RFB greeting sent to Mode II viewers before they transmit ID:xxxx.
	mode2ViewerGreeting = "RFB 000.000\n"
	// mode2HandshakeTimeout is the maximum time allowed to complete the ID:xxxx handshake.
	mode2HandshakeTimeout = 10 * time.Second
)

type pendingServer struct {
	mode2ID     int
	conn        net.Conn
	connectedAt time.Time
}

// Mode2Manager implements the UltraVNC Repeater Mode II reverse ID matchmaker.
// Target VNC servers behind NAT connect outbound to Mode2ReversePort (default 5500) and send "ID:xxxx".
// VNC viewers connect to Mode2ViewerPort (default 5901), receive "RFB 000.000\n", send "ID:xxxx",
// and are spliced directly to the waiting target server socket.
type Mode2Manager struct {
	cfg        *config.Config
	meter      *TrafficMeter
	log        *slog.Logger
	mu         sync.Mutex
	reverseLn  net.Listener
	viewerLn   net.Listener
	pending    map[int]*pendingServer
	stopCh     chan struct{}
	wg         sync.WaitGroup
	stopped    bool
}

// NewMode2Manager initializes a Mode 2 UltraVNC reverse ID matchmaker.
func NewMode2Manager(cfg *config.Config, meter *TrafficMeter, log *slog.Logger) *Mode2Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Mode2Manager{
		cfg:     cfg,
		meter:   meter,
		log:     log,
		pending: make(map[int]*pendingServer),
		stopCh:  make(chan struct{}),
	}
}

// Start opens the Mode 2 reverse server listener and viewer listener if Mode2Enabled is true.
func (m *Mode2Manager) Start() error {
	if !m.cfg.Features.Mode2Enabled {
		m.log.Info("Mode 2 repeater disabled by feature flag")
		return nil
	}

	bindHost := m.cfg.Server.VNCBind
	if bindHost == "" {
		bindHost = "0.0.0.0"
	}

	revAddr := net.JoinHostPort(bindHost, fmt.Sprintf("%d", m.cfg.Server.Mode2ReversePort))
	revLn, err := net.Listen("tcp", revAddr)
	if err != nil {
		return fmt.Errorf("listen Mode 2 reverse server port %s: %w", revAddr, err)
	}

	viewAddr := net.JoinHostPort(bindHost, fmt.Sprintf("%d", m.cfg.Server.Mode2ViewerPort))
	viewLn, err := net.Listen("tcp", viewAddr)
	if err != nil {
		_ = revLn.Close()
		return fmt.Errorf("listen Mode 2 viewer port %s: %w", viewAddr, err)
	}

	m.mu.Lock()
	m.reverseLn = revLn
	m.viewerLn = viewLn
	m.mu.Unlock()

	m.log.Info("started Mode 2 UltraVNC repeater listeners",
		"reverse_server_listen", revLn.Addr().String(),
		"viewer_listen", viewLn.Addr().String(),
	)

	m.wg.Add(2)
	go m.acceptReverseLoop(revLn)
	go m.acceptViewerLoop(viewLn)

	return nil
}

func (m *Mode2Manager) acceptReverseLoop(ln net.Listener) {
	defer m.wg.Done()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-m.stopCh:
				return
			default:
				if errors.Is(err, net.ErrClosed) {
					return
				}
				m.log.Warn("Mode 2 reverse server accept error", "err", err)
				continue
			}
		}

		m.wg.Add(1)
		go func(c net.Conn) {
			defer m.wg.Done()
			m.handleReverseServer(c)
		}(conn)
	}
}

func (m *Mode2Manager) acceptViewerLoop(ln net.Listener) {
	defer m.wg.Done()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-m.stopCh:
				return
			default:
				if errors.Is(err, net.ErrClosed) {
					return
				}
				m.log.Warn("Mode 2 viewer accept error", "err", err)
				continue
			}
		}

		m.wg.Add(1)
		go func(c net.Conn) {
			defer m.wg.Done()
			m.handleViewer(c)
		}(conn)
	}
}

func (m *Mode2Manager) handleReverseServer(serverConn net.Conn) {
	remoteIP := ExtractRemoteIP(serverConn.RemoteAddr())
	if !m.cfg.IsIPAllowed(remoteIP) {
		m.log.Warn("rejected Mode 2 reverse server outside CIDR allowlist",
			"server_ip", serverConn.RemoteAddr().String(),
		)
		_ = serverConn.Close()
		return
	}

	ConfigureTCPConn(serverConn, m.cfg.ConnectionLimits.TCPKeepAliveSec)

	mode2ID, err := readMode2ID(serverConn, mode2HandshakeTimeout)
	if err != nil {
		m.log.Warn("invalid Mode 2 reverse server handshake",
			"server_ip", serverConn.RemoteAddr().String(),
			"err", err,
		)
		_ = serverConn.Close()
		return
	}

	// Verify if a registered device with this Mode2ID is explicitly disabled
	if dev, found := m.findDeviceByMode2ID(mode2ID); found && !dev.Enabled {
		m.log.Warn("rejected Mode 2 reverse server for disabled device",
			"device_id", dev.ID,
			"mode2_id", mode2ID,
		)
		_ = serverConn.Close()
		return
	}

	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		_ = serverConn.Close()
		return
	}
	if prev, exists := m.pending[mode2ID]; exists {
		_ = prev.conn.Close()
		m.log.Info("replaced previous waiting Mode 2 reverse server", "mode2_id", mode2ID)
	}
	m.pending[mode2ID] = &pendingServer{
		mode2ID:     mode2ID,
		conn:        serverConn,
		connectedAt: time.Now(),
	}
	m.mu.Unlock()

	m.log.Info("Mode 2 reverse VNC server registered and waiting for viewer",
		"mode2_id", mode2ID,
		"server_addr", serverConn.RemoteAddr().String(),
	)
}

func (m *Mode2Manager) handleViewer(viewerConn net.Conn) {
	remoteIP := ExtractRemoteIP(viewerConn.RemoteAddr())
	if !m.cfg.IsIPAllowed(remoteIP) {
		m.log.Warn("rejected Mode 2 viewer outside CIDR allowlist",
			"viewer_ip", viewerConn.RemoteAddr().String(),
		)
		_ = viewerConn.Close()
		return
	}

	ConfigureTCPConn(viewerConn, m.cfg.ConnectionLimits.TCPKeepAliveSec)

	_ = viewerConn.SetWriteDeadline(time.Now().Add(mode2HandshakeTimeout))
	if _, err := io.WriteString(viewerConn, mode2ViewerGreeting); err != nil {
		m.log.Warn("failed to write Mode 2 viewer greeting",
			"viewer_ip", viewerConn.RemoteAddr().String(),
			"err", err,
		)
		_ = viewerConn.Close()
		return
	}
	_ = viewerConn.SetWriteDeadline(time.Time{})

	mode2ID, err := readMode2ID(viewerConn, mode2HandshakeTimeout)
	if err != nil {
		m.log.Warn("invalid Mode 2 viewer ID handshake",
			"viewer_ip", viewerConn.RemoteAddr().String(),
			"err", err,
		)
		_ = viewerConn.Close()
		return
	}

	dev, found := m.findDeviceByMode2ID(mode2ID)
	if found && !dev.Enabled {
		m.log.Warn("rejected Mode 2 viewer for disabled device",
			"device_id", dev.ID,
			"mode2_id", mode2ID,
		)
		_ = viewerConn.Close()
		return
	}

	m.mu.Lock()
	ps, ok := m.pending[mode2ID]
	if ok {
		delete(m.pending, mode2ID)
	}
	m.mu.Unlock()

	if !ok || ps == nil {
		m.log.Warn("Mode 2 viewer requested ID with no waiting VNC server",
			"mode2_id", mode2ID,
			"viewer_ip", viewerConn.RemoteAddr().String(),
		)
		_ = viewerConn.Close()
		return
	}

	serverConn := ps.conn
	deviceID := fmt.Sprintf("mode2-%d", mode2ID)
	deviceName := fmt.Sprintf("ID:%d", mode2ID)
	if found {
		deviceID = dev.ID
		deviceName = dev.Name
	}

	closeBoth := func() {
		_ = viewerConn.Close()
		_ = serverConn.Close()
	}

	var sess *Session
	if m.meter != nil {
		sess = m.meter.RegisterSession(
			deviceID,
			deviceName,
			"mode2",
			viewerConn.RemoteAddr().String(),
			serverConn.RemoteAddr().String(),
			closeBoth,
		)
		defer m.meter.UnregisterSession(sess.ID)
	}

	m.log.Info("Mode 2 session matched and spliced",
		"session_id", func() string {
			if sess != nil {
				return sess.ID
			}
			return "unmetered"
		}(),
		"mode2_id", mode2ID,
		"device_id", deviceID,
		"viewer", viewerConn.RemoteAddr().String(),
		"server", serverConn.RemoteAddr().String(),
	)

	ForwardStream(
		viewerConn,
		serverConn,
		sess,
		m.cfg.ConnectionLimits,
		m.cfg.Features.BandwidthMeteringEnabled,
	)

	m.log.Info("Mode 2 session closed",
		"mode2_id", mode2ID,
		"device_id", deviceID,
	)
}

func (m *Mode2Manager) findDeviceByMode2ID(mode2ID int) (config.DeviceConfig, bool) {
	for _, dev := range m.cfg.GetDevices() {
		if dev.Mode2ID == mode2ID {
			return dev, true
		}
	}
	return config.DeviceConfig{}, false
}

// HasPendingServer reports whether a reverse VNC server is currently waiting in the pool for mode2ID.
func (m *Mode2Manager) HasPendingServer(mode2ID int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.pending[mode2ID]
	return ok
}

// ReverseAddr returns the bound address of the Mode 2 reverse server listener, or empty if not running.
func (m *Mode2Manager) ReverseAddr() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reverseLn != nil {
		return m.reverseLn.Addr().String()
	}
	return ""
}

// ViewerAddr returns the bound address of the Mode 2 viewer listener, or empty if not running.
func (m *Mode2Manager) ViewerAddr() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.viewerLn != nil {
		return m.viewerLn.Addr().String()
	}
	return ""
}

// Stop closes both Mode 2 listeners, disconnects any waiting reverse servers, and waits for handlers.
func (m *Mode2Manager) Stop() {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return
	}
	m.stopped = true
	close(m.stopCh)
	if m.reverseLn != nil {
		_ = m.reverseLn.Close()
	}
	if m.viewerLn != nil {
		_ = m.viewerLn.Close()
	}
	for id, ps := range m.pending {
		_ = ps.conn.Close()
		delete(m.pending, id)
	}
	m.mu.Unlock()

	m.wg.Wait()
}

// readMode2ID reads the UltraVNC Mode II "ID:xxxx" handshake from conn.
// It supports both standard 250-byte null-padded buffers (draining all 250 bytes so no
// padding leaks into the RFB stream) and newline-terminated strings ("ID:1234\n").
func readMode2ID(conn net.Conn, timeout time.Duration) (int, error) {
	if timeout > 0 {
		_ = conn.SetReadDeadline(time.Now().Add(timeout))
		defer func() {
			_ = conn.SetReadDeadline(time.Time{})
		}()
	}

	var buf [mode2PacketLen]byte
	total := 0

	for total < mode2PacketLen {
		n, err := conn.Read(buf[total:])
		if n > 0 {
			total += n
			chunk := buf[:total]
			// If the peer sent a newline-terminated line without null padding, stop reading immediately
			// so we never consume subsequent RFB protocol bytes.
			if bytes.IndexByte(chunk, '\x00') == -1 && bytes.IndexByte(chunk, '\n') >= 0 {
				break
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) && total > 0 {
				break
			}
			return 0, fmt.Errorf("read Mode 2 ID: %w", err)
		}
	}

	return parseMode2ID(buf[:total])
}

func parseMode2ID(raw []byte) (int, error) {
	// Truncate at first null byte or newline
	if idx := bytes.IndexByte(raw, '\x00'); idx >= 0 {
		raw = raw[:idx]
	}
	if idx := bytes.IndexByte(raw, '\n'); idx >= 0 {
		raw = raw[:idx]
	}
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return 0, errors.New("empty Mode 2 ID payload")
	}

	upper := strings.ToUpper(s)
	if strings.HasPrefix(upper, "ID:") {
		s = strings.TrimSpace(s[3:])
	}

	id, err := strconv.Atoi(s)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid Mode 2 numeric ID %q", s)
	}
	return id, nil
}
