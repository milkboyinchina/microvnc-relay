package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStandaloneTargetsLoadAndSave(t *testing.T) {
	tmpDir := t.TempDir()
	targetsPath := filepath.Join(tmpDir, "targets.yaml")

	devices := []DeviceConfig{
		{
			ID:          "test-pi",
			Name:        "Test Pi",
			Host:        "192.168.1.100",
			Port:        5900,
			VirtualPort: 5915,
			Enabled:     true,
		},
		{
			ID:      "test-mode2",
			Name:    "Mode2 Only",
			Mode2ID: 9999,
			Enabled: true,
		},
	}

	if err := SaveTargetsFile(targetsPath, devices); err != nil {
		t.Fatalf("SaveTargetsFile failed: %v", err)
	}

	loaded, err := LoadTargetsFile(targetsPath)
	if err != nil {
		t.Fatalf("LoadTargetsFile failed: %v", err)
	}

	if len(loaded) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(loaded))
	}
	if loaded[0].ID != "test-pi" || loaded[1].Mode2ID != 9999 {
		t.Errorf("unexpected loaded targets content: %+v", loaded)
	}
}

func TestDualConfigLoadPrecedence(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	targetsPath := filepath.Join(tmpDir, "targets.yaml")

	configYAML := `
server:
  web_bind: "127.0.0.1:8080"
devices:
  - id: "inline-device"
    host: "10.0.0.1"
    port: 5900
    enabled: true
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0640); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	// 1. Without targets.yaml, inline devices from config.yaml are loaded
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load config failed: %v", err)
	}
	if len(cfg.Devices) != 1 || cfg.Devices[0].ID != "inline-device" {
		t.Errorf("expected inline device, got %+v", cfg.Devices)
	}

	// 2. Now create targets.yaml -> it takes precedence over inline devices
	standaloneTargets := []DeviceConfig{
		{
			ID:          "standalone-target",
			Host:        "10.0.0.2",
			Port:        5900,
			VirtualPort: 5930,
			Enabled:     true,
		},
	}
	if err := SaveTargetsFile(targetsPath, standaloneTargets); err != nil {
		t.Fatalf("SaveTargetsFile failed: %v", err)
	}

	cfg2, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load config with targets.yaml failed: %v", err)
	}
	if len(cfg2.Devices) != 1 || cfg2.Devices[0].ID != "standalone-target" {
		t.Errorf("expected standalone target, got %+v", cfg2.Devices)
	}
}
