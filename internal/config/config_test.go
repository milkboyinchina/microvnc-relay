package config

import (
	"net"
	"os"
	"path/filepath"
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
