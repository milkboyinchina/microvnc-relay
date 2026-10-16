package telemetry

import (
	"io"
	"log/slog"
	"net"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/milkboyinchina/microvnc-relay/internal/config"
)

func TestExtractTCPInfoRTT(t *testing.T) {
	// 1. Nil connection should return 0, nil
	if rtt, err := ExtractTCPInfoRTT(nil); err != nil || rtt != 0 {
		t.Fatalf("expected (0, nil) for nil conn, got (%v, %v)", rtt, err)
	}

	// 2. Non-TCP net.Pipe should return 0, nil
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	if rtt, err := ExtractTCPInfoRTT(c1); err != nil || rtt != 0 {
		t.Fatalf("expected (0, nil) for net.Pipe, got (%v, %v)", rtt, err)
	}

	// 3. Real TCP loopback socket
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	defer ln.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv, err := ln.Accept()
		if err != nil {
			return
		}
		defer srv.Close()
		_, _ = srv.Write([]byte("RFB 003.008\n"))
	}()

	cli, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial tcp: %v", err)
	}
	defer cli.Close()

	buf := make([]byte, 12)
	_, _ = io.ReadFull(cli, buf)
	<-done

	rtt, err := ExtractTCPInfoRTT(cli)
	if err != nil {
		t.Fatalf("ExtractTCPInfoRTT failed: %v", err)
	}
	if runtime.GOOS == "linux" && rtt <= 0 {
		t.Errorf("expected positive TCP_INFO RTT on Linux loopback, got %v", rtt)
	}
}

func TestPingerOnlineOfflineAndPruning(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen mock target: %v", err)
	}
	defer ln.Close()

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	onlinePort := ln.Addr().(*net.TCPAddr).Port

	// Allocate and immediately close a port to guarantee an offline port
	closedLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen closed port: %v", err)
	}
	offlinePort := closedLn.Addr().(*net.TCPAddr).Port
	_ = closedLn.Close()

	cfg := config.DefaultConfig()
	cfg.ConnectionLimits.DialTimeoutSec = 1
	cfg.Devices = []config.DeviceConfig{
		{
			ID:      "dev-online",
			Name:    "Online Target",
			Host:    "127.0.0.1",
			Port:    onlinePort,
			Enabled: true,
		},
		{
			ID:      "dev-offline",
			Name:    "Offline Target",
			Host:    "127.0.0.1",
			Port:    offlinePort,
			Enabled: true,
		},
		{
			ID:      "dev-mode2",
			Name:    "Mode 2 NAT Target",
			Host:    "127.0.0.1",
			Port:    offlinePort,
			Mode2ID: 9999,
			Enabled: true,
		},
	}

	var callbackCount atomic.Int32
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pinger := NewPinger(
		cfg,
		log,
		func(deviceID string, rtt time.Duration) {
			if deviceID == "dev-online" {
				callbackCount.Add(1)
			}
		},
		func(mode2ID int) bool {
			return mode2ID == 9999
		},
	)

	pinger.ProbeAll()

	stOnline, ok := pinger.GetStatus("dev-online")
	if !ok || !stOnline.Online {
		t.Fatalf("expected dev-online to be online, got %+v", stOnline)
	}
	if callbackCount.Load() != 1 {
		t.Errorf("expected onTargetRTT callback once, got %d", callbackCount.Load())
	}

	stOffline, ok := pinger.GetStatus("dev-offline")
	if !ok || stOffline.Online || stOffline.LastError == "" {
		t.Fatalf("expected dev-offline to be offline with error, got %+v", stOffline)
	}

	stMode2, ok := pinger.GetStatus("dev-mode2")
	if !ok || !stMode2.Online {
		t.Fatalf("expected dev-mode2 with pending server to be online, got %+v", stMode2)
	}

	// Remove dev-offline and verify ProbeAll prunes it from the map
	if !cfg.DeleteDevice("dev-offline") {
		t.Fatal("DeleteDevice('dev-offline') returned false")
	}
	pinger.ProbeAll()
	if _, stillExists := pinger.GetStatus("dev-offline"); stillExists {
		t.Error("expected deleted device dev-offline to be pruned from Pinger statuses")
	}
}

func TestPingerFeatureFlagDisabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Features.LatencyProbesEnabled = false
	cfg.Devices = []config.DeviceConfig{
		{
			ID:      "dev-1",
			Name:    "Target 1",
			Host:    "127.0.0.1",
			Port:    5900,
			Enabled: true,
		},
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pinger := NewPinger(cfg, log, nil, nil)
	pinger.Start()
	defer pinger.Stop()

	if len(pinger.GetAllStatuses()) != 0 {
		t.Fatalf("expected 0 statuses when latency_probes_enabled=false, got %d", len(pinger.GetAllStatuses()))
	}
}
