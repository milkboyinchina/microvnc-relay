package config

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadExampleConfig(t *testing.T) {
	// Root config.example.yaml must load and validate cleanly
	cfg, err := Load("../../config.example.yaml")
	if err != nil {
		t.Fatalf("Failed to load config.example.yaml: %v", err)
	}

	if !cfg.Features.WebUIEnabled {
		t.Errorf("Expected WebUIEnabled to be true in config.example.yaml")
	}
	if !cfg.Features.TopologyMapEnabled {
		t.Errorf("Expected TopologyMapEnabled to be true in config.example.yaml")
	}
	if len(cfg.Devices) != 3 {
		t.Errorf("Expected 3 devices in config.example.yaml, got %d", len(cfg.Devices))
	}

	dev, ok := cfg.FindDevice("pi2-lab")
	if !ok {
		t.Fatalf("Device 'pi2-lab' not found")
	}
	if dev.Port != 5900 || dev.VirtualPort != 5910 {
		t.Errorf("Unexpected device ports: port=%d, virtual_port=%d", dev.Port, dev.VirtualPort)
	}
}

func TestAccessControlFiltering(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AccessControl.Enabled = true
	cfg.AccessControl.AllowedSubnets = []string{
		"100.64.0.0/10", // NetBird
		"192.168.1.0/24",
		"127.0.0.1",
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}

	tests := []struct {
		ip      string
		allowed bool
	}{
		{"100.64.0.5", true},
		{"100.127.255.254", true},
		{"192.168.1.50", true},
		{"192.168.2.1", false},
		{"127.0.0.1", true},
		{"8.8.8.8", false},
	}

	for _, tc := range tests {
		ip := net.ParseIP(tc.ip)
		if allowed := cfg.IsIPAllowed(ip); allowed != tc.allowed {
			t.Errorf("IsIPAllowed(%s) = %v; want %v", tc.ip, allowed, tc.allowed)
		}
	}
}

func TestValidationErrors(t *testing.T) {
	t.Run("Duplicate Device ID", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Devices = []DeviceConfig{
			{ID: "dev1", Host: "10.0.0.1", Port: 5900},
			{ID: "dev1", Host: "10.0.0.2", Port: 5900},
		}
		if err := cfg.Validate(); err == nil {
			t.Errorf("Expected error for duplicate device ID, got nil")
		}
	})

	t.Run("Virtual Port Conflict", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Devices = []DeviceConfig{
			{ID: "dev1", Host: "10.0.0.1", Port: 5900, VirtualPort: 5910},
			{ID: "dev2", Host: "10.0.0.2", Port: 5900, VirtualPort: 5910},
		}
		if err := cfg.Validate(); err == nil {
			t.Errorf("Expected error for virtual port conflict, got nil")
		}
	})

	t.Run("Mode 2 ID Conflict", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Devices = []DeviceConfig{
			{ID: "dev1", Host: "10.0.0.1", Port: 5900, Mode2ID: 1234},
			{ID: "dev2", Host: "10.0.0.2", Port: 5900, Mode2ID: 1234},
		}
		if err := cfg.Validate(); err == nil {
			t.Errorf("Expected error for mode2 ID conflict, got nil")
		}
	})

	t.Run("Invalid Subnet", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.AccessControl.Enabled = true
		cfg.AccessControl.AllowedSubnets = []string{"not-an-ip"}
		if err := cfg.Validate(); err == nil {
			t.Errorf("Expected error for invalid CIDR subnet, got nil")
		}
	})

	t.Run("Virtual Port Conflicts With WebBind", func(t *testing.T) {
		cfg := DefaultConfig() // WebBind defaults to 0.0.0.0:8080
		cfg.Devices = []DeviceConfig{
			{ID: "dev1", Host: "10.0.0.1", Port: 5900, VirtualPort: 8080},
		}
		if err := cfg.Validate(); err == nil {
			t.Errorf("Expected error for virtual_port colliding with web_bind :8080, got nil")
		}
	})

	t.Run("Virtual Port Conflicts With Mode2 Ports", func(t *testing.T) {
		cfg := DefaultConfig() // Mode2ReversePort=5500, Mode2ViewerPort=5901
		cfg.Devices = []DeviceConfig{
			{ID: "dev1", Host: "10.0.0.1", Port: 5900, VirtualPort: 5500},
		}
		if err := cfg.Validate(); err == nil {
			t.Errorf("Expected error for virtual_port colliding with mode2_reverse_port :5500, got nil")
		}
		cfg.Devices[0].VirtualPort = 5901
		if err := cfg.Validate(); err == nil {
			t.Errorf("Expected error for virtual_port colliding with mode2_viewer_port :5901, got nil")
		}
	})
}

func TestAtomicSaveAndReload(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "sub", "config.yaml")

	cfg := DefaultConfig()
	cfg.Server.WebBind = "127.0.0.1:9090"
	cfg.Devices = []DeviceConfig{
		{ID: "test-box", Name: "Test Box", Host: "192.168.1.100", Port: 5900, VirtualPort: 5950, Enabled: true},
	}

	if err := cfg.Save(configPath); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("Saved config file does not exist: %v", err)
	}

	// Reload and verify
	reloaded, err := Load(configPath)
	if err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	if reloaded.Server.WebBind != "127.0.0.1:9090" {
		t.Errorf("WebBind = %q; want %q", reloaded.Server.WebBind, "127.0.0.1:9090")
	}
	if len(reloaded.Devices) != 1 || reloaded.Devices[0].ID != "test-box" {
		t.Errorf("Reloaded devices mismatch: %+v", reloaded.Devices)
	}
}

func TestDeviceCRUD(t *testing.T) {
	cfg := DefaultConfig()

	// Add device
	dev := DeviceConfig{ID: "dev-a", Host: "192.168.1.10", Port: 5900, VirtualPort: 5915, Enabled: true}
	if err := cfg.AddOrUpdateDevice(dev); err != nil {
		t.Fatalf("Add device failed: %v", err)
	}

	// Attempt to add a conflicting device on the same virtual_port -> must fail and roll back
	conflictDev := DeviceConfig{ID: "dev-b", Host: "192.168.1.11", Port: 5900, VirtualPort: 5915, Enabled: true}
	if err := cfg.AddOrUpdateDevice(conflictDev); err == nil {
		t.Fatalf("Expected port collision error when adding dev-b on port 5915")
	}
	if len(cfg.GetDevices()) != 1 {
		t.Fatalf("Expected rollback to preserve 1 device, got %d", len(cfg.GetDevices()))
	}

	if d, ok := cfg.FindDeviceByVirtualPort(5915); !ok || d.ID != "dev-a" {
		t.Errorf("FindDeviceByVirtualPort failed, got: %+v, ok=%v", d, ok)
	}

	// Update device
	dev.Port = 5901
	if err := cfg.AddOrUpdateDevice(dev); err != nil {
		t.Fatalf("Update device failed: %v", err)
	}
	if d, ok := cfg.FindDevice("dev-a"); !ok || d.Port != 5901 {
		t.Errorf("Device port not updated: %+v", d)
	}

	// Delete device
	if !cfg.DeleteDevice("dev-a") {
		t.Errorf("DeleteDevice returned false for existing device")
	}
	if _, ok := cfg.FindDevice("dev-a"); ok {
		t.Errorf("Device 'dev-a' still exists after deletion")
	}
}

func TestLoadResolvesServerTargetsFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := `
server:
  web_bind: "127.0.0.1:8080"
  vnc_bind: "127.0.0.1"
  targets_file: "custom-targets.yaml"
features:
  mode1_enabled: false
  mode2_enabled: false
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0640); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(dir, "custom-targets.yaml")
	if got := cfg.TargetsPath(); got != want {
		t.Fatalf("targets path = %q, want %q", got, want)
	}
}

func TestAdmissionLimitDefaultsAndCeilings(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ConnectionLimits.MaxTotalSessions = 0
	cfg.ConnectionLimits.MaxSessionsPerDevice = 0
	cfg.ConnectionLimits.MaxPendingHandshakes = 0
	cfg.ConnectionLimits.MaxWSClients = 0
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate defaults: %v", err)
	}
	if cfg.ConnectionLimits.MaxTotalSessions != 32 ||
		cfg.ConnectionLimits.MaxSessionsPerDevice != 8 ||
		cfg.ConnectionLimits.MaxPendingHandshakes != 64 ||
		cfg.ConnectionLimits.MaxWSClients != 8 {
		t.Fatalf("unexpected admission defaults: %+v", cfg.ConnectionLimits)
	}

	for name, mutate := range map[string]func(*ConnectionLimitsConfig){
		"max_total_sessions":      func(l *ConnectionLimitsConfig) { l.MaxTotalSessions = 257 },
		"max_sessions_per_device": func(l *ConnectionLimitsConfig) { l.MaxSessionsPerDevice = 65 },
		"max_pending_handshakes":  func(l *ConnectionLimitsConfig) { l.MaxPendingHandshakes = 513 },
		"max_ws_clients":          func(l *ConnectionLimitsConfig) { l.MaxWSClients = 65 },
	} {
		bad := DefaultConfig()
		mutate(&bad.ConnectionLimits)
		if err := bad.Validate(); err == nil {
			t.Errorf("expected %s above ceiling to be rejected", name)
		}
	}

	negative := DefaultConfig()
	negative.ConnectionLimits.MaxTotalSessions = -1
	if err := negative.Validate(); err == nil {
		t.Fatal("expected negative max_total_sessions to be rejected")
	}
}

func TestApplyDeviceTransactionPersistsCandidate(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.targetsPath = filepath.Join(dir, "targets.yaml")

	newDev := DeviceConfig{
		ID:          "new-dev",
		Host:        "127.0.0.1",
		Port:        5900,
		VirtualPort: 5910,
		Enabled:     true,
	}
	if err := cfg.ApplyDeviceTransaction(func(devices []DeviceConfig) ([]DeviceConfig, error) {
		return append(devices, newDev), nil
	}); err != nil {
		t.Fatalf("ApplyDeviceTransaction: %v", err)
	}

	persisted, err := LoadTargetsFile(cfg.targetsPath)
	if err != nil {
		t.Fatalf("LoadTargetsFile: %v", err)
	}
	if len(persisted) != 1 || persisted[0].ID != "new-dev" {
		t.Fatalf("unexpected persisted devices: %+v", persisted)
	}
	if len(cfg.GetDevices()) != 1 || cfg.GetDevices()[0].ID != "new-dev" {
		t.Fatalf("unexpected in-memory devices: %+v", cfg.GetDevices())
	}
}

func TestApplyDeviceTransactionRollsBackOnPersistFailure(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	cfg := DefaultConfig()
	cfg.targetsPath = filepath.Join(blocker, "targets.yaml")
	cfg.Devices = []DeviceConfig{{
		ID:          "keep-me",
		Host:        "127.0.0.1",
		Port:        5900,
		VirtualPort: 5910,
		Enabled:     true,
	}}

	err := cfg.ApplyDeviceTransaction(func(devices []DeviceConfig) ([]DeviceConfig, error) {
		return append(devices, DeviceConfig{
			ID:          "must-not-appear",
			Host:        "127.0.0.1",
			Port:        5901,
			VirtualPort: 5911,
			Enabled:     true,
		}), nil
	})
	if !errors.Is(err, ErrPersistence) {
		t.Fatalf("expected ErrPersistence, got %v", err)
	}

	devices := cfg.GetDevices()
	if len(devices) != 1 || devices[0].ID != "keep-me" {
		t.Fatalf("failed transaction mutated in-memory devices: %+v", devices)
	}
}

func TestApplyReloadAppliesAccessControl(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AccessControl.Enabled = false
	if err := cfg.Validate(); err != nil {
		t.Fatalf("initial Validate: %v", err)
	}

	newCfg := DefaultConfig()
	newCfg.AccessControl.Enabled = true
	newCfg.AccessControl.AllowedSubnets = []string{"10.0.0.0/8"}
	if err := newCfg.Validate(); err != nil {
		t.Fatalf("new Validate: %v", err)
	}

	warnings, err := cfg.ApplyReload(newCfg)
	if err != nil {
		t.Fatalf("ApplyReload: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if !cfg.AccessControl.Enabled {
		t.Fatal("access control was not enabled by reload")
	}
	if !cfg.IsIPAllowed(net.ParseIP("10.1.2.3")) {
		t.Fatal("expected reloaded subnet to allow 10.1.2.3")
	}
	if cfg.IsIPAllowed(net.ParseIP("192.168.1.1")) {
		t.Fatal("expected reloaded subnet to reject 192.168.1.1")
	}
}

func TestApplyReloadRejectsInvalidCandidateAndPreservesState(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Devices = []DeviceConfig{{
		ID: "keep", Host: "127.0.0.1", Port: 5900, VirtualPort: 5910, Enabled: true,
	}}

	invalid := DefaultConfig()
	invalid.Devices = []DeviceConfig{
		{ID: "a", Host: "127.0.0.1", Port: 5900, VirtualPort: 5910, Enabled: true},
		{ID: "b", Host: "127.0.0.1", Port: 5901, VirtualPort: 5910, Enabled: true},
	}

	if _, err := cfg.ApplyReload(invalid); err == nil {
		t.Fatal("expected invalid reload to fail")
	}
	devices := cfg.GetDevices()
	if len(devices) != 1 || devices[0].ID != "keep" {
		t.Fatalf("invalid reload mutated state: %+v", devices)
	}
}

func TestApplyReloadWarnsOnRestartOnlyChange(t *testing.T) {
	cfg := DefaultConfig()
	newCfg := DefaultConfig()
	newCfg.Server.WebBind = "127.0.0.1:9999"

	warnings, err := cfg.ApplyReload(newCfg)
	if err != nil {
		t.Fatalf("ApplyReload: %v", err)
	}
	found := false
	for _, warning := range warnings {
		if strings.Contains(warning, "server") && strings.Contains(warning, "restart") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a restart warning for web_bind change, got %v", warnings)
	}
	if cfg.Server.WebBind == "127.0.0.1:9999" {
		t.Fatal("restart-only server setting must not be applied by reload")
	}
}
