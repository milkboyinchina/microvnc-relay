package proxy

import (
	"testing"

	"github.com/milkboyinchina/microvnc-relay/internal/config"
)

func TestAdmissionControllerGlobalCap(t *testing.T) {
	ac := NewAdmissionController(config.ConnectionLimitsConfig{
		MaxTotalSessions:     2,
		MaxSessionsPerDevice: 10,
	})

	releaseA, ok := ac.TryAcquire("a")
	if !ok {
		t.Fatal("expected first session to be admitted")
	}
	releaseB, ok := ac.TryAcquire("b")
	if !ok {
		t.Fatal("expected second session to be admitted")
	}
	if _, ok := ac.TryAcquire("c"); ok {
		t.Fatal("expected third session to be rejected by the global cap")
	}

	snap := ac.Snapshot()
	if snap.TotalSessions != 2 {
		t.Fatalf("expected 2 active sessions, got %d", snap.TotalSessions)
	}
	if snap.RejectedGlobal != 1 {
		t.Fatalf("expected 1 global rejection, got %d", snap.RejectedGlobal)
	}

	releaseA()
	releaseA() // idempotent
	if _, ok := ac.TryAcquire("c"); !ok {
		t.Fatal("expected admission to succeed after release")
	}
	releaseB()
}

func TestAdmissionControllerPerDeviceCap(t *testing.T) {
	ac := NewAdmissionController(config.ConnectionLimitsConfig{
		MaxTotalSessions:     10,
		MaxSessionsPerDevice: 1,
	})

	releaseA, ok := ac.TryAcquire("device-a")
	if !ok {
		t.Fatal("expected first device-a session to be admitted")
	}
	if _, ok := ac.TryAcquire("device-a"); ok {
		t.Fatal("expected second device-a session to be rejected")
	}
	releaseB, ok := ac.TryAcquire("device-b")
	if !ok {
		t.Fatal("expected device-b session to be admitted while device-a is saturated")
	}

	snap := ac.Snapshot()
	if snap.RejectedPerDevice != 1 {
		t.Fatalf("expected 1 per-device rejection, got %d", snap.RejectedPerDevice)
	}
	if snap.PerDevice["device-a"] != 1 || snap.PerDevice["device-b"] != 1 {
		t.Fatalf("unexpected per-device counts: %+v", snap.PerDevice)
	}

	releaseA()
	releaseB()
	if snap := ac.Snapshot(); snap.TotalSessions != 0 || len(snap.PerDevice) != 0 {
		t.Fatalf("expected empty admission state after release, got %+v", snap)
	}
}

func TestHandshakeSemaphoreSharedByMode1AndMode2(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ConnectionLimits.MaxPendingHandshakes = 1
	meter := NewTrafficMeter(false)

	m1 := NewMode1Manager(cfg, meter, nil)
	m2 := NewMode2Manager(cfg, meter, nil)

	shared := NewSemaphore(1)
	m1.SetHandshakeSemaphore(shared)
	m2.SetHandshakeSemaphore(shared)

	if m1.handshakeSem != shared {
		t.Fatal("Mode 1 did not adopt the shared handshake semaphore")
	}
	if m2.handshakeSem != shared {
		t.Fatal("Mode 2 did not adopt the shared handshake semaphore")
	}

	// A slot taken by one mode must be visible to the other; the configured
	// cap bounds the process, not each listener group.
	if !m1.handshakeSem.TryAcquire() {
		t.Fatal("expected Mode 1 to acquire the shared slot")
	}
	if m2.handshakeSem.TryAcquire() {
		t.Fatal("expected Mode 2 to be rejected while Mode 1 holds the only slot")
	}
	m1.handshakeSem.Release()
	if !m2.handshakeSem.TryAcquire() {
		t.Fatal("expected Mode 2 to acquire after Mode 1 released")
	}
	m2.handshakeSem.Release()
}

func TestSemaphoreTryAcquireAndRejectedCounter(t *testing.T) {
	sem := NewSemaphore(1)
	if !sem.TryAcquire() {
		t.Fatal("expected first acquire to succeed")
	}
	if sem.TryAcquire() {
		t.Fatal("expected second acquire to fail")
	}
	if sem.Rejected() != 1 {
		t.Fatalf("expected 1 rejection, got %d", sem.Rejected())
	}
	sem.Release()
	if !sem.TryAcquire() {
		t.Fatal("expected acquire to succeed after release")
	}
	sem.Release()
	sem.Release() // idempotent
}
