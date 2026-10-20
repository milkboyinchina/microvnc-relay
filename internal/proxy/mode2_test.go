package proxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/milkboyinchina/microvnc-relay/internal/config"
)

func startTestMode2(t *testing.T, mutate func(*config.Config)) (*Mode2Manager, *config.Config, *TrafficMeter) {
	t.Helper()

	cfg := config.DefaultConfig()
	cfg.Server.VNCBind = "127.0.0.1"
	cfg.Server.Mode2ReversePort = getFreePort(t)
	cfg.Server.Mode2ViewerPort = getFreePort(t)
	cfg.Features.Mode2Enabled = true
	cfg.Features.BandwidthMeteringEnabled = false
	cfg.Features.LatencyProbesEnabled = false
	if mutate != nil {
		mutate(cfg)
	}

	meter := NewTrafficMeter(false)
	m := NewMode2Manager(cfg, meter, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := m.Start(); err != nil {
		t.Fatalf("Mode2Manager.Start: %v", err)
	}
	t.Cleanup(func() {
		_ = m.Stop(context.Background())
	})
	return m, cfg, meter
}

func writeMode2ID(t *testing.T, conn net.Conn, id int) {
	t.Helper()
	var hs [mode2PacketLen]byte
	copy(hs[:], fmt.Sprintf("ID:%d", id))
	if _, err := conn.Write(hs[:]); err != nil {
		t.Fatalf("write Mode 2 ID %d: %v", id, err)
	}
}

func readMode2Greeting(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var greeting [12]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil {
		t.Fatalf("read Mode 2 greeting: %v", err)
	}
	_ = conn.SetReadDeadline(time.Time{})
	if string(greeting[:]) != mode2ViewerGreeting {
		t.Fatalf("unexpected greeting: %q", greeting[:])
	}
}

func waitForMode2(t *testing.T, timeout time.Duration, fn func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func TestMode2ViewerWaitsThenPairsWithLateServer(t *testing.T) {
	m, _, _ := startTestMode2(t, func(cfg *config.Config) {
		cfg.ConnectionLimits.Mode2ViewerWaitTimeoutSec = 3
	})

	viewer, err := net.DialTimeout("tcp", m.ViewerAddr(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial viewer: %v", err)
	}
	defer viewer.Close()
	readMode2Greeting(t, viewer)
	writeMode2ID(t, viewer, 5001)

	if !waitForMode2(t, 2*time.Second, func() bool { return m.PendingViewerCount() == 1 }) {
		t.Fatal("viewer was not queued while waiting for a server")
	}

	server, err := net.DialTimeout("tcp", m.ReverseAddr(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial reverse server: %v", err)
	}
	defer server.Close()
	writeMode2ID(t, server, 5001)

	if !waitForMode2(t, 2*time.Second, func() bool {
		return m.PendingViewerCount() == 0 && m.PendingServerCount() == 0
	}) {
		t.Fatal("late reverse server did not pair with the waiting viewer")
	}

	serverDone := make(chan error, 1)
	go func() {
		_ = server.SetReadDeadline(time.Now().Add(3 * time.Second))
		var ping [4]byte
		if _, err := io.ReadFull(server, ping[:]); err != nil {
			serverDone <- err
			return
		}
		if string(ping[:]) != "PING" {
			serverDone <- fmt.Errorf("server got %q, want PING", ping[:])
			return
		}
		_, err := server.Write([]byte("PONG"))
		serverDone <- err
	}()

	if _, err := viewer.Write([]byte("PING")); err != nil {
		t.Fatalf("viewer write PING: %v", err)
	}
	_ = viewer.SetReadDeadline(time.Now().Add(3 * time.Second))
	var pong [4]byte
	if _, err := io.ReadFull(viewer, pong[:]); err != nil {
		t.Fatalf("viewer read PONG: %v", err)
	}
	if string(pong[:]) != "PONG" {
		t.Fatalf("viewer got %q, want PONG", pong[:])
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server side: %v", err)
	}
}

func TestMode2ViewerWaitTimeout(t *testing.T) {
	m, _, _ := startTestMode2(t, func(cfg *config.Config) {
		cfg.ConnectionLimits.Mode2ViewerWaitTimeoutSec = 1
	})

	viewer, err := net.DialTimeout("tcp", m.ViewerAddr(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial viewer: %v", err)
	}
	defer viewer.Close()
	readMode2Greeting(t, viewer)
	writeMode2ID(t, viewer, 5002)

	if !waitForMode2(t, 2*time.Second, func() bool { return m.PendingViewerCount() == 1 }) {
		t.Fatal("viewer was not queued")
	}

	_ = viewer.SetReadDeadline(time.Now().Add(3 * time.Second))
	var one [1]byte
	if _, err := viewer.Read(one[:]); err == nil {
		t.Fatal("expected viewer to be closed after wait timeout")
	}
	if !waitForMode2(t, 2*time.Second, func() bool { return m.PendingViewerCount() == 0 }) {
		t.Fatal("timed-out viewer was not removed from the queue")
	}
}

func TestMode2HandshakeDeadlineClosesSlowloris(t *testing.T) {
	m, _, _ := startTestMode2(t, func(cfg *config.Config) {
		cfg.ConnectionLimits.Mode2HandshakeTimeoutSec = 1
	})

	// Reverse listener: partial ID with no delimiter.
	reverse, err := net.DialTimeout("tcp", m.ReverseAddr(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial reverse: %v", err)
	}
	defer reverse.Close()
	if _, err := reverse.Write([]byte("ID:12")); err != nil {
		t.Fatalf("write partial reverse ID: %v", err)
	}
	_ = reverse.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := reverse.Read(make([]byte, 1)); err == nil {
		t.Fatal("expected reverse slowloris to be disconnected")
	}

	// Viewer listener: greeting then partial ID.
	viewer, err := net.DialTimeout("tcp", m.ViewerAddr(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial viewer: %v", err)
	}
	defer viewer.Close()
	readMode2Greeting(t, viewer)
	if _, err := viewer.Write([]byte("ID:12")); err != nil {
		t.Fatalf("write partial viewer ID: %v", err)
	}
	_ = viewer.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := viewer.Read(make([]byte, 1)); err == nil {
		t.Fatal("expected viewer slowloris to be disconnected")
	}
}

func TestMode2DuplicateServerIDEvictsOlder(t *testing.T) {
	m, _, _ := startTestMode2(t, nil)

	first, err := net.DialTimeout("tcp", m.ReverseAddr(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial first reverse: %v", err)
	}
	defer first.Close()
	writeMode2ID(t, first, 5003)

	if !waitForMode2(t, 2*time.Second, func() bool { return m.PendingServerCount() == 1 }) {
		t.Fatal("first reverse server was not registered")
	}

	second, err := net.DialTimeout("tcp", m.ReverseAddr(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial second reverse: %v", err)
	}
	defer second.Close()
	writeMode2ID(t, second, 5003)

	// The older waiting server must be closed by the takeover.
	_ = first.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := first.Read(make([]byte, 1)); err == nil {
		t.Fatal("expected older duplicate reverse server to be evicted")
	}

	viewer, err := net.DialTimeout("tcp", m.ViewerAddr(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial viewer: %v", err)
	}
	defer viewer.Close()
	readMode2Greeting(t, viewer)
	writeMode2ID(t, viewer, 5003)

	serverDone := make(chan error, 1)
	go func() {
		_ = second.SetReadDeadline(time.Now().Add(3 * time.Second))
		var ping [4]byte
		if _, err := io.ReadFull(second, ping[:]); err != nil {
			serverDone <- err
			return
		}
		_, err := second.Write([]byte("PONG"))
		serverDone <- err
	}()

	if _, err := viewer.Write([]byte("PING")); err != nil {
		t.Fatalf("viewer write: %v", err)
	}
	_ = viewer.SetReadDeadline(time.Now().Add(3 * time.Second))
	var pong [4]byte
	if _, err := io.ReadFull(viewer, pong[:]); err != nil {
		t.Fatalf("viewer read: %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("newest server side: %v", err)
	}
}

func TestMode2DuplicateViewerIDEvictsOlder(t *testing.T) {
	m, _, _ := startTestMode2(t, func(cfg *config.Config) {
		cfg.ConnectionLimits.Mode2ViewerWaitTimeoutSec = 3
	})

	first, err := net.DialTimeout("tcp", m.ViewerAddr(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial first viewer: %v", err)
	}
	defer first.Close()
	readMode2Greeting(t, first)
	writeMode2ID(t, first, 5004)

	if !waitForMode2(t, 2*time.Second, func() bool { return m.PendingViewerCount() == 1 }) {
		t.Fatal("first viewer was not queued")
	}

	second, err := net.DialTimeout("tcp", m.ViewerAddr(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial second viewer: %v", err)
	}
	defer second.Close()
	readMode2Greeting(t, second)
	writeMode2ID(t, second, 5004)

	_ = first.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := first.Read(make([]byte, 1)); err == nil {
		t.Fatal("expected older duplicate viewer to be evicted")
	}
	if !waitForMode2(t, 2*time.Second, func() bool { return m.PendingViewerCount() == 1 }) {
		t.Fatal("expected exactly one queued viewer after eviction")
	}

	server, err := net.DialTimeout("tcp", m.ReverseAddr(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial reverse: %v", err)
	}
	defer server.Close()
	writeMode2ID(t, server, 5004)

	serverDone := make(chan error, 1)
	go func() {
		_ = server.SetReadDeadline(time.Now().Add(3 * time.Second))
		var ping [4]byte
		if _, err := io.ReadFull(server, ping[:]); err != nil {
			serverDone <- err
			return
		}
		_, err := server.Write([]byte("PONG"))
		serverDone <- err
	}()

	if _, err := second.Write([]byte("PING")); err != nil {
		t.Fatalf("viewer write: %v", err)
	}
	_ = second.SetReadDeadline(time.Now().Add(3 * time.Second))
	var pong [4]byte
	if _, err := io.ReadFull(second, pong[:]); err != nil {
		t.Fatalf("viewer read: %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server side: %v", err)
	}
}
