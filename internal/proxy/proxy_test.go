package proxy

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/milkboyinchina/microvnc-relay/internal/config"
)

func getFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to allocate free port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// startMockRFBServer starts a local TCP server that sends "RFB 003.008\n" and echoes back client bytes.
func startMockRFBServer(t *testing.T) (int, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen mock RFB server: %v", err)
	}

	stopCh := make(chan struct{})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-stopCh:
					return
				default:
					return
				}
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = c.Write([]byte("RFB 003.008\n"))
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()

	port := ln.Addr().(*net.TCPAddr).Port
	return port, func() {
		close(stopCh)
		_ = ln.Close()
	}
}

func TestMode1ManagerEndToEndAndReload(t *testing.T) {
	targetPort, stopTarget := startMockRFBServer(t)
	defer stopTarget()

	virtualPort1 := getFreePort(t)
	virtualPort2 := getFreePort(t)

	cfg := config.DefaultConfig()
	cfg.Server.VNCBind = "127.0.0.1"
	cfg.Features.Mode1Enabled = true
	cfg.Features.BandwidthMeteringEnabled = true
	cfg.Devices = []config.DeviceConfig{
		{
			ID:          "pi-signage-01",
			Name:        "Lobby Signage",
			Host:        "127.0.0.1",
			Port:        targetPort,
			VirtualPort: virtualPort1,
			Enabled:     true,
		},
	}

	meter := NewTrafficMeter(true)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m1 := NewMode1Manager(cfg, meter, log)

	if err := m1.Start(); err != nil {
		t.Fatalf("Mode1Manager.Start failed: %v", err)
	}
	defer m1.Stop()

	listenAddr := m1.ListenerAddr("pi-signage-01")
	if listenAddr == "" {
		t.Fatal("expected non-empty ListenerAddr for pi-signage-01")
	}

	clientConn, err := net.DialTimeout("tcp", listenAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial Mode 1 virtual port: %v", err)
	}
	defer clientConn.Close()

	// Expect RFB greeting from mock target
	greeting := make([]byte, 12)
	if _, err := io.ReadFull(clientConn, greeting); err != nil {
		t.Fatalf("read RFB greeting: %v", err)
	}
	if string(greeting) != "RFB 003.008\n" {
		t.Fatalf("unexpected RFB greeting: %q", string(greeting))
	}

	// Send 64KB payload and read echo
	payload := bytes.Repeat([]byte("VNCFRAME"), 8192) // 65,536 bytes
	if _, err := clientConn.Write(payload); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	echo := make([]byte, len(payload))
	if _, err := io.ReadFull(clientConn, echo); err != nil {
		t.Fatalf("read echoed payload: %v", err)
	}
	if !bytes.Equal(payload, echo) {
		t.Fatal("echoed payload mismatch")
	}

	// Verify active session metrics
	sessions := meter.GetSessions()
	if len(sessions) != 1 {
		t.Fatalf("expected 1 active session, got %d", len(sessions))
	}
	if sessions[0].RxBytesTotal != uint64(len(payload)) {
		t.Errorf("expected RxBytesTotal=%d, got %d", len(payload), sessions[0].RxBytesTotal)
	}
	if sessions[0].TxBytesTotal != uint64(12+len(payload)) {
		t.Errorf("expected TxBytesTotal=%d, got %d", 12+len(payload), sessions[0].TxBytesTotal)
	}

	// Test dynamic Reload: add second device and remove first device
	updatedDevices := []config.DeviceConfig{
		{
			ID:          "pi-signage-02",
			Name:        "Kiosk 2",
			Host:        "127.0.0.1",
			Port:        targetPort,
			VirtualPort: virtualPort2,
			Enabled:     true,
		},
	}
	if err := m1.Reload(updatedDevices); err != nil {
		t.Fatalf("Mode1Manager.Reload failed: %v", err)
	}
	if m1.ListenerAddr("pi-signage-01") != "" {
		t.Error("expected pi-signage-01 listener to be stopped after Reload")
	}
	if m1.ListenerAddr("pi-signage-02") == "" {
		t.Error("expected pi-signage-02 listener to be started after Reload")
	}

	// Force disconnect active session via TrafficMeter
	if !meter.DisconnectSession(sessions[0].SessionID) {
		t.Error("expected DisconnectSession to return true")
	}

	// Wait for session unregistration
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(meter.GetSessions()) == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(meter.GetSessions()) != 0 {
		t.Fatalf("expected 0 active sessions after disconnect, got %d", len(meter.GetSessions()))
	}
}

func TestMode1CIDRRejection(t *testing.T) {
	targetPort, stopTarget := startMockRFBServer(t)
	defer stopTarget()
	virtualPort := getFreePort(t)

	cfg := config.DefaultConfig()
	cfg.Server.VNCBind = "127.0.0.1"
	cfg.AccessControl.Enabled = true
	cfg.AccessControl.AllowedSubnets = []string{"10.99.0.0/16"} // Excludes 127.0.0.1
	cfg.Devices = []config.DeviceConfig{
		{
			ID:          "restricted-dev",
			Name:        "Restricted",
			Host:        "127.0.0.1",
			Port:        targetPort,
			VirtualPort: virtualPort,
			Enabled:     true,
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("cfg.Validate: %v", err)
	}

	meter := NewTrafficMeter(true)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m1 := NewMode1Manager(cfg, meter, log)
	if err := m1.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m1.Stop()

	conn, err := net.DialTimeout("tcp", m1.ListenerAddr("restricted-dev"), 2*time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 16)
	_, err = conn.Read(buf)
	if err == nil {
		t.Fatal("expected immediate EOF/close for client outside CIDR allowlist")
	}
}

func TestMode2UltraVNCMatchmaker(t *testing.T) {
	revPort := getFreePort(t)
	viewPort := getFreePort(t)

	cfg := config.DefaultConfig()
	cfg.Server.VNCBind = "127.0.0.1"
	cfg.Server.Mode2ReversePort = revPort
	cfg.Server.Mode2ViewerPort = viewPort
	cfg.Features.Mode2Enabled = true
	cfg.Features.BandwidthMeteringEnabled = true
	cfg.Devices = []config.DeviceConfig{
		{
			ID:      "nat-target-01",
			Name:    "Remote NAT Rig",
			Host:    "10.0.0.50",
			Port:    5900,
			Mode2ID: 1234,
			Enabled: true,
		},
	}

	meter := NewTrafficMeter(true)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m2 := NewMode2Manager(cfg, meter, log)
	if err := m2.Start(); err != nil {
		t.Fatalf("Mode2Manager.Start: %v", err)
	}
	defer m2.Stop()

	// 1. Connect synthetic reverse VNC server to Mode2ReversePort sending 250-byte null-padded "ID:1234"
	revConn, err := net.DialTimeout("tcp", m2.ReverseAddr(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial Mode 2 reverse port: %v", err)
	}
	defer revConn.Close()

	var serverHandshake [250]byte
	copy(serverHandshake[:], "ID:1234")
	if _, err := revConn.Write(serverHandshake[:]); err != nil {
		t.Fatalf("write reverse server handshake: %v", err)
	}

	// Wait until Mode2Manager registers pending server 1234
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if m2.HasPendingServer(1234) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !m2.HasPendingServer(1234) {
		t.Fatal("expected HasPendingServer(1234) == true")
	}

	// Start mock RFB responder on revConn once paired
	serverDone := make(chan error, 1)
	go func() {
		if _, err := revConn.Write([]byte("RFB 003.008\n")); err != nil {
			serverDone <- err
			return
		}
		buf := make([]byte, 4)
		if _, err := io.ReadFull(revConn, buf); err != nil {
			serverDone <- err
			return
		}
		if string(buf) != "PING" {
			serverDone <- io.ErrUnexpectedEOF
			return
		}
		_, err := revConn.Write([]byte("PONG"))
		serverDone <- err
	}()

	// 2. Connect synthetic UltraVNC viewer to Mode2ViewerPort
	viewConn, err := net.DialTimeout("tcp", m2.ViewerAddr(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial Mode 2 viewer port: %v", err)
	}
	defer viewConn.Close()

	// Viewer must first receive "RFB 000.000\n" from repeater
	repeaterGreeting := make([]byte, 12)
	if _, err := io.ReadFull(viewConn, repeaterGreeting); err != nil {
		t.Fatalf("read repeater greeting: %v", err)
	}
	if string(repeaterGreeting) != "RFB 000.000\n" {
		t.Fatalf("unexpected repeater greeting: %q", string(repeaterGreeting))
	}

	// Viewer sends 250-byte "ID:1234" handshake
	var viewerHandshake [250]byte
	copy(viewerHandshake[:], "ID:"+strconv.Itoa(1234))
	if _, err := viewConn.Write(viewerHandshake[:]); err != nil {
		t.Fatalf("write viewer handshake: %v", err)
	}

	// Viewer should now receive the target server's "RFB 003.008\n" banner cleanly (no leaked null bytes!)
	serverBanner := make([]byte, 12)
	if _, err := io.ReadFull(viewConn, serverBanner); err != nil {
		t.Fatalf("read spliced server banner: %v", err)
	}
	if string(serverBanner) != "RFB 003.008\n" {
		t.Fatalf("expected 'RFB 003.008\\n', got %q", string(serverBanner))
	}

	if _, err := viewConn.Write([]byte("PING")); err != nil {
		t.Fatalf("viewer write PING: %v", err)
	}
	pong := make([]byte, 4)
	if _, err := io.ReadFull(viewConn, pong); err != nil {
		t.Fatalf("viewer read PONG: %v", err)
	}
	if string(pong) != "PONG" {
		t.Fatalf("expected PONG, got %q", string(pong))
	}

	if err := <-serverDone; err != nil {
		t.Fatalf("reverse server goroutine error: %v", err)
	}

	_ = viewConn.Close()
	_ = revConn.Close()
}

func TestForwardStreamQoSAndIdleTimeout(t *testing.T) {
	c1, c2 := net.Pipe()
	t1, t2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	defer t1.Close()
	defer t2.Close()

	limits := config.ConnectionLimitsConfig{
		TCPKeepAliveSec: 10,
		DialTimeoutSec:  2,
		IdleTimeoutSec:  1,
		MaxSessionKBps:  512,
	}

	meter := NewTrafficMeter(true)
	sess := meter.RegisterSession("dev-1", "Dev 1", "mode1", "127.0.0.1:1000", "127.0.0.1:5900", func() {
		_ = c2.Close()
		_ = t1.Close()
	})
	defer meter.UnregisterSession(sess.ID)

	done := make(chan struct{})
	go func() {
		defer close(done)
		ForwardStream(c2, t1, sess, limits, true)
	}()

	// Exchange data over the userspace pooled buffer path
	go func() {
		_, _ = c1.Write([]byte("hello-rfb"))
	}()
	recv := make([]byte, 9)
	if _, err := io.ReadFull(t2, recv); err != nil {
		t.Fatalf("read from t2: %v", err)
	}
	if string(recv) != "hello-rfb" {
		t.Fatalf("unexpected data: %q", string(recv))
	}

	// Let the 1s idle timeout expire and verify ForwardStream terminates automatically
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("expected ForwardStream to close after 1s IdleTimeoutSec")
	}
}

func TestMode1PortCollisionTracking(t *testing.T) {
	// Occupy a port externally to simulate EADDRINUSE
	externalLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to bind external port: %v", err)
	}
	defer externalLn.Close()
	occupiedPort := externalLn.Addr().(*net.TCPAddr).Port

	freePort := getFreePort(t)

	cfg := config.DefaultConfig()
	cfg.Server.VNCBind = "127.0.0.1"
	cfg.Devices = []config.DeviceConfig{
		{
			ID:          "collision-dev",
			Name:        "Collision Target",
			Host:        "127.0.0.1",
			Port:        5900,
			VirtualPort: occupiedPort,
			Enabled:     true,
		},
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m1 := NewMode1Manager(cfg, nil, log)
	defer m1.Stop()

	// Start should return an error and record the bind error for "collision-dev"
	if err := m1.Start(); err == nil {
		t.Fatalf("expected Start to return bind error for occupied port %d", occupiedPort)
	}

	bindErrs := m1.GetBindErrors()
	if bindErrs["collision-dev"] == "" {
		t.Fatalf("expected bind error recorded for 'collision-dev', got: %+v", bindErrs)
	}

	// Reconfigure to a free port and reload -> bind error must be cleared
	cfg.Devices[0].VirtualPort = freePort
	if err := m1.Reload(cfg.Devices); err != nil {
		t.Fatalf("Reload on free port failed: %v", err)
	}

	bindErrsAfter := m1.GetBindErrors()
	if len(bindErrsAfter) != 0 {
		t.Fatalf("expected bind errors cleared after resolving port collision, got: %+v", bindErrsAfter)
	}
}

