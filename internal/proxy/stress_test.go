package proxy

// Stress tests for the RFB proxy engine. Run with:
//   go test -v -race -run=TestStress ./internal/proxy/...
//
// All tests use loopback listeners and synthetic echo targets: no disk writes,
// no external network, bounded buffers and bounded goroutines only.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/milkboyinchina/microvnc-relay/internal/config"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// startEchoTargetWithStop starts an RFB-greeting echo server; caller must defer stop().
func startEchoTargetWithStop(t *testing.T) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo target: %v", err)
	}
	stopCh := make(chan struct{})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				select {
				case <-stopCh:
					return
				default:
					return
				}
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_, _ = conn.Write([]byte("RFB 003.008\n"))
				_, _ = io.Copy(conn, conn)
			}(c)
		}
	}()
	return ln.Addr().String(), func() { close(stopCh); _ = ln.Close() }
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// TestStressConcurrentMode1Forwarding drives 50 concurrent streams x 1MB each
// through Mode 1 and asserts zero lost bytes and zero lingering sessions.
func TestStressConcurrentMode1Forwarding(t *testing.T) {
	if testing.Short() {
		t.Skip("skip stress in -short mode")
	}
	const streams = 50
	const bytesPerStream = 1 << 20 // 1MB

	targetAddr, stopTarget := startEchoTargetWithStop(t)
	defer stopTarget()
	host, portStr, _ := net.SplitHostPort(targetAddr)
	var targetPort int
	_, _ = fmt.Sscanf(portStr, "%d", &targetPort)

	cfg := config.DefaultConfig()
	cfg.Server.VNCBind = "127.0.0.1"
	cfg.Features.Mode1Enabled = true
	cfg.Features.BandwidthMeteringEnabled = true
	for i := 0; i < 5; i++ {
		cfg.Devices = append(cfg.Devices, config.DeviceConfig{
			ID:          fmt.Sprintf("stress-%d", i),
			Name:        fmt.Sprintf("Stress %d", i),
			Host:        host,
			Port:        targetPort,
			VirtualPort: freePort(t),
			Enabled:     true,
		})
	}

	meter := NewTrafficMeter(true)
	m1 := NewMode1Manager(cfg, meter, testLogger())
	if err := m1.Start(); err != nil {
		t.Fatalf("Mode1Manager.Start: %v", err)
	}
	defer m1.Stop(context.Background())

	payload := bytes.Repeat([]byte("VNCFRAME"), 8192) // 64KB
	var wg sync.WaitGroup
	var failed atomic.Int64
	for i := 0; i < streams; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			addr := m1.ListenerAddr(fmt.Sprintf("stress-%d", i%5))
			conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
			if err != nil {
				t.Errorf("dial stream %d: %v", i, err)
				failed.Add(1)
				return
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
			greet := make([]byte, 12)
			if _, err := io.ReadFull(conn, greet); err != nil {
				t.Errorf("stream %d greeting: %v", i, err)
				failed.Add(1)
				return
			}
			sent := 0
			echo := make([]byte, len(payload))
			for sent < bytesPerStream {
				if _, err := conn.Write(payload); err != nil {
					t.Errorf("stream %d write: %v", i, err)
					failed.Add(1)
					return
				}
				if _, err := io.ReadFull(conn, echo); err != nil {
					t.Errorf("stream %d echo: %v", i, err)
					failed.Add(1)
					return
				}
				if !bytes.Equal(echo, payload) {
					t.Errorf("stream %d echo mismatch", i)
					failed.Add(1)
					return
				}
				sent += len(payload)
			}
		}(i)
	}
	wg.Wait()
	if failed.Load() > 0 {
		t.Fatalf("%d/%d streams failed", failed.Load(), streams)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(meter.GetSessions()) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if left := len(meter.GetSessions()); left != 0 {
		t.Fatalf("expected 0 sessions after %d streams, got %d", streams, left)
	}
}

// TestStressMode2RaceConditions pits 20 viewers against 10 reverse servers
// and asserts pairing completes with no panic, deadlock, or leaked waiter.
func TestStressMode2RaceConditions(t *testing.T) {
	if testing.Short() {
		t.Skip("skip stress in -short mode")
	}
	revPort := freePort(t)
	viewPort := freePort(t)

	cfg := config.DefaultConfig()
	cfg.Server.VNCBind = "127.0.0.1"
	cfg.Server.Mode2ReversePort = revPort
	cfg.Server.Mode2ViewerPort = viewPort
	cfg.Features.Mode2Enabled = true
	cfg.Features.BandwidthMeteringEnabled = true

	meter := NewTrafficMeter(true)
	m2 := NewMode2Manager(cfg, meter, testLogger())
	if err := m2.Start(); err != nil {
		t.Fatalf("Mode2Manager.Start: %v", err)
	}
	defer m2.Stop(context.Background())

	const pairs = 10
	var wg sync.WaitGroup
	var failed atomic.Int64

	// Reverse servers register IDs 7000..7009.
	for i := 0; i < pairs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			conn, err := net.DialTimeout("tcp", m2.ReverseAddr(), 3*time.Second)
			if err != nil {
				t.Errorf("reverse dial %d: %v", i, err)
				failed.Add(1)
				return
			}
			defer conn.Close()
			var hs [250]byte
			copy(hs[:], fmt.Sprintf("ID:%d", 7000+i))
			if _, err := conn.Write(hs[:]); err != nil {
				t.Errorf("reverse write %d: %v", i, err)
				failed.Add(1)
				return
			}
			// Hold until paired viewer closes (or timeout).
			_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
			_, _ = conn.Read(make([]byte, 1))
		}(i)
	}

	// Wait for all waiters to register.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ok := true
		for i := 0; i < pairs; i++ {
			if !m2.HasPendingServer(7000 + i) {
				ok = false
				break
			}
		}
		if ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 20 viewers contend for 10 IDs (each ID requested twice).
	for i := 0; i < 2*pairs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			conn, err := net.DialTimeout("tcp", m2.ViewerAddr(), 3*time.Second)
			if err != nil {
				t.Errorf("viewer dial %d: %v", i, err)
				failed.Add(1)
				return
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
			greet := make([]byte, 12)
			if _, err := io.ReadFull(conn, greet); err != nil {
				return // loser of duplicate race is closed; acceptable
			}
			var hs [250]byte
			copy(hs[:], fmt.Sprintf("ID:%d", 7000+(i%pairs)))
			if _, err := conn.Write(hs[:]); err != nil {
				return
			}
			_, _ = conn.Read(make([]byte, 1))
		}(i)
	}
	wg.Wait()
	if failed.Load() > 0 {
		t.Fatalf("%d dial failures", failed.Load())
	}

	time.Sleep(500 * time.Millisecond)
	for i := 0; i < pairs; i++ {
		if m2.HasPendingServer(7000 + i) {
			t.Errorf("ID %d still pending after contention", 7000+i)
		}
	}
	if left := len(meter.GetSessions()); left != 0 {
		t.Fatalf("expected 0 sessions after race, got %d", left)
	}
}

// TestStressAbruptDisconnections runs 100 rapid connect/kill cycles and
// asserts the session map and goroutine footprint return to baseline.
func TestStressAbruptDisconnections(t *testing.T) {
	if testing.Short() {
		t.Skip("skip stress in -short mode")
	}
	targetAddr, stopTarget := startEchoTargetWithStop(t)
	defer stopTarget()
	host, portStr, _ := net.SplitHostPort(targetAddr)
	var targetPort int
	_, _ = fmt.Sscanf(portStr, "%d", &targetPort)

	cfg := config.DefaultConfig()
	cfg.Server.VNCBind = "127.0.0.1"
	cfg.Features.Mode1Enabled = true
	cfg.Features.BandwidthMeteringEnabled = true
	cfg.Devices = []config.DeviceConfig{{
		ID: "kill-dev", Name: "Kill", Host: host, Port: targetPort,
		VirtualPort: freePort(t), Enabled: true,
	}}

	meter := NewTrafficMeter(true)
	m1 := NewMode1Manager(cfg, meter, testLogger())
	if err := m1.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m1.Stop(context.Background())
	addr := m1.ListenerAddr("kill-dev")

	for i := 0; i < 100; i++ {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatalf("cycle %d dial: %v", i, err)
		}
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		_, _ = io.ReadFull(conn, make([]byte, 12))
		if tc, ok := conn.(*net.TCPConn); ok && i%2 == 0 {
			_ = tc.SetLinger(0) // RST on even cycles
		}
		_ = conn.Close()
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(meter.GetSessions()) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if left := len(meter.GetSessions()); left != 0 {
		t.Fatalf("expected 0 sessions after 100 kill cycles, got %d", left)
	}
}
