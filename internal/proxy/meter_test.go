package proxy

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestTrafficMeterAndSessionLifecycle(t *testing.T) {
	tm := NewTrafficMeter(true)

	disconnected := false
	sess := tm.RegisterSession("pi2-lab", "Raspberry Pi 2", "mode1", "100.64.0.5:51234", "192.168.1.50:5900", func() {
		disconnected = true
	})

	sess.SetClientRTT(12500 * time.Microsecond) // 12.5 ms
	sess.SetTargetRTT(1200 * time.Microsecond)  // 1.2 ms

	sess.AddRx(4096)
	sess.AddTx(65536)

	// Run 1Hz sample tick
	tm.SampleTick()

	snap := tm.Snapshot(false)
	if snap.TotalCumulativeRx != 4096 || snap.TotalCumulativeTx != 65536 {
		t.Errorf("unexpected cumulative totals: rx=%d, tx=%d", snap.TotalCumulativeRx, snap.TotalCumulativeTx)
	}
	if snap.TotalRxBytesSec != 4096 || snap.TotalTxBytesSec != 65536 {
		t.Errorf("unexpected per-sec rates: rx/s=%d, tx/s=%d", snap.TotalRxBytesSec, snap.TotalTxBytesSec)
	}

	sessions := tm.GetSessions()
	if len(sessions) != 1 {
		t.Fatalf("expected 1 active session, got %d", len(sessions))
	}
	if sessions[0].ClientRTTMs != 12.5 || sessions[0].TargetRTTMs != 1.2 {
		t.Errorf("unexpected RTTs: client=%v, target=%v", sessions[0].ClientRTTMs, sessions[0].TargetRTTMs)
	}

	// Simulate 59 more ticks to complete 1 minute bucket
	for i := 0; i < 59; i++ {
		tm.SampleTick()
	}

	snapWithHist := tm.Snapshot(true)
	if len(snapWithHist.History24h) != 1 {
		t.Fatalf("expected 1 history bucket after 60 ticks, got %d", len(snapWithHist.History24h))
	}
	if snapWithHist.History24h[0].RxBytes != 4096 || snapWithHist.History24h[0].TxBytes != 65536 {
		t.Errorf("unexpected bucket totals: %+v", snapWithHist.History24h[0])
	}

	// Test disconnect via TrafficMeter
	if !tm.DisconnectSession(sess.ID) || !disconnected {
		t.Errorf("DisconnectSession failed to invoke callback")
	}

	// Reset counters
	tm.ResetCounters()
	afterReset := tm.Snapshot(true)
	if afterReset.TotalCumulativeRx != 0 || len(afterReset.History24h) != 0 {
		t.Errorf("expected zeroed counters and history after ResetCounters, got %+v", afterReset)
	}

	tm.UnregisterSession(sess.ID)
	if tm.ActiveSessionCountForDevice("pi2-lab") != 0 {
		t.Errorf("expected 0 active sessions after unregister")
	}
}

func TestMeteredConnWrapper(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	var readBytes, writeBytes uint64
	mc := NewMeteredConn(c1, func(n uint64) { readBytes += n }, func(n uint64) { writeBytes += n })

	go func() {
		_, _ = c2.Write([]byte("RFB 003.008\n"))
		buf := make([]byte, 12)
		_, _ = c2.Read(buf)
	}()

	inBuf := make([]byte, 12)
	n, err := mc.Read(inBuf)
	if err != nil || n != 12 {
		t.Fatalf("mc.Read failed: n=%d, err=%v", n, err)
	}

	n, err = mc.Write([]byte("RFB 003.008\n"))
	if err != nil || n != 12 {
		t.Fatalf("mc.Write failed: n=%d, err=%v", n, err)
	}

	if readBytes != 12 || writeBytes != 12 {
		t.Errorf("expected readBytes=12, writeBytes=12; got read=%d, write=%d", readBytes, writeBytes)
	}
}

func TestTrafficMeterRejectsRegistrationDuringShutdown(t *testing.T) {
	tm := NewTrafficMeter(false)
	tm.BeginShutdown()

	sess := tm.RegisterSession("dev", "Device", "mode1", "client", "target", func() {})
	if sess != nil {
		t.Fatal("expected nil session registration after BeginShutdown")
	}
	if tm.ActiveSessionCount() != 0 {
		t.Fatalf("expected 0 sessions, got %d", tm.ActiveSessionCount())
	}
}

func TestTrafficMeterShutdownDisconnectsAndWaits(t *testing.T) {
	tm := NewTrafficMeter(false)
	disconnected := make(chan struct{})

	sess := tm.RegisterSession("dev", "Device", "mode1", "client", "target", func() {
		close(disconnected)
	})
	if sess == nil {
		t.Fatal("expected session registration to succeed before shutdown")
	}

	go func() {
		<-disconnected
		tm.UnregisterSession(sess.ID)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	remaining, err := tm.Shutdown(ctx)
	if err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected no remaining sessions, got %v", remaining)
	}
	if tm.ActiveSessionCount() != 0 {
		t.Fatalf("expected 0 active sessions, got %d", tm.ActiveSessionCount())
	}
}

func TestTrafficMeterShutdownTimeoutReportsRemaining(t *testing.T) {
	tm := NewTrafficMeter(false)
	sess := tm.RegisterSession("dev", "Device", "mode1", "client", "target", func() {})
	if sess == nil {
		t.Fatal("expected session registration to succeed before shutdown")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	remaining, err := tm.Shutdown(ctx)
	if err == nil {
		t.Fatal("expected shutdown timeout for a session that never unregisters")
	}
	if len(remaining) != 1 || remaining[0] != sess.ID {
		t.Fatalf("expected remaining session %q, got %v", sess.ID, remaining)
	}
}
