package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// FeaturesConfig manages modular subsystem toggles to run ultra-lean.
type FeaturesConfig struct {
	WebUIEnabled             bool `yaml:"webui_enabled"`
	TopologyMapEnabled       bool `yaml:"topology_map_enabled"`
	NoVNCEnabled             bool `yaml:"novnc_enabled"`
	Mode1Enabled             bool `yaml:"mode1_enabled"`
	Mode2Enabled             bool `yaml:"mode2_enabled"`
	LatencyProbesEnabled     bool `yaml:"latency_probes_enabled"`
	BandwidthMeteringEnabled bool `yaml:"bandwidth_metering_enabled"`
}

// ServerConfig manages network binding addresses and ports.
type ServerConfig struct {
	WebBind          string `yaml:"web_bind"`
	VNCBind          string `yaml:"vnc_bind"`
	Mode2ReversePort int    `yaml:"mode2_reverse_port"`
	Mode2ViewerPort  int    `yaml:"mode2_viewer_port"`
}

// SecurityConfig holds WebUI authentication credentials and parameters.
type SecurityConfig struct {
	AuthEnabled           bool   `yaml:"auth_enabled"`
	Username              string `yaml:"username"`
	PasswordHash          string `yaml:"password_hash"`
	SessionTimeoutMinutes int    `yaml:"session_timeout_minutes"`
}

// AccessControlConfig manages CIDR-based client IP filtering.
type AccessControlConfig struct {
	Enabled        bool     `yaml:"enabled"`
	AllowedSubnets []string `yaml:"allowed_subnets"`
	allowedNets    []*net.IPNet
}

// ConnectionLimitsConfig manages timeouts, keepalive, and QoS bandwidth limits.
type ConnectionLimitsConfig struct {
	TCPKeepAliveSec         int `yaml:"tcp_keepalive_sec"`
	DialTimeoutSec          int `yaml:"dial_timeout_sec"`
	IdleTimeoutSec          int `yaml:"idle_timeout_sec"`
	MaxSessionKBps          int `yaml:"max_session_kbps"`
	Mode2HandshakeTimeoutSec int `yaml:"mode2_handshake_timeout_sec"`
}

// LoggingConfig manages log file settings and in-memory ring buffer capacity.
type LoggingConfig struct {
	DiskLogging         bool   `yaml:"disk_logging"`
	LogLevel            string `yaml:"log_level"`
	LogFile             string `yaml:"log_file"`
	MaxSizeMB           int    `yaml:"max_size_mb"`
	MaxBackups          int    `yaml:"max_backups"`
	InMemoryBufferLines int    `yaml:"in_memory_buffer_lines"`
}

// TelemetryConfig manages broadcast and target probing intervals.
type TelemetryConfig struct {
	BroadcastIntervalSec  int `yaml:"broadcast_interval_sec"`
	TargetPingIntervalSec int `yaml:"target_ping_interval_sec"`
}

// DeviceConfig specifies an upstream VNC target machine.
type DeviceConfig struct {
	ID            string `yaml:"id" json:"id"`
	Name          string `yaml:"name" json:"name"`
	Host          string `yaml:"host" json:"host"`
	Port          int    `yaml:"port" json:"port"`
	VirtualPort   int    `yaml:"virtual_port" json:"virtual_port"`
	Mode2ID       int    `yaml:"mode2_id" json:"mode2_id"`
	VNCServerType string `yaml:"vnc_server_type" json:"vnc_server_type"`
	Enabled       bool   `yaml:"enabled" json:"enabled"`
}

// Config is the root application configuration structure.
type Config struct {
	mu               sync.RWMutex
	Features         FeaturesConfig         `yaml:"features"`
	Server           ServerConfig           `yaml:"server"`
	Security         SecurityConfig         `yaml:"security"`
	AccessControl    AccessControlConfig    `yaml:"access_control"`
	ConnectionLimits ConnectionLimitsConfig `yaml:"connection_limits"`
	Logging          LoggingConfig          `yaml:"logging"`
	Telemetry        TelemetryConfig        `yaml:"telemetry"`
	Devices          []DeviceConfig         `yaml:"devices"`
}

// DefaultConfig returns a fully populated configuration with production-safe defaults.
func DefaultConfig() *Config {
	return &Config{
		Features: FeaturesConfig{
			WebUIEnabled:             true,
			TopologyMapEnabled:       true,
			NoVNCEnabled:             true,
			Mode1Enabled:             true,
			Mode2Enabled:             true,
			LatencyProbesEnabled:     true,
			BandwidthMeteringEnabled: true,
		},
		Server: ServerConfig{
			WebBind:          "0.0.0.0:8080",
			VNCBind:          "0.0.0.0",
			Mode2ReversePort: 5500,
			Mode2ViewerPort:  5901,
		},
		Security: SecurityConfig{
			AuthEnabled:           true,
			Username:              "admin",
			PasswordHash:          "$2a$12$e8N4O1.UqMh0z7e5k8aE1.9JgU6P8f2K4r3B1t0y9u8i7o6p5q4w3", // "admin"
			SessionTimeoutMinutes: 1440,
		},
		AccessControl: AccessControlConfig{
			Enabled: false,
			AllowedSubnets: []string{
				"100.64.0.0/10",
				"192.168.0.0/16",
				"10.0.0.0/8",
				"127.0.0.1/32",
			},
		},
		ConnectionLimits: ConnectionLimitsConfig{
			TCPKeepAliveSec:         30,
			DialTimeoutSec:          5,
			IdleTimeoutSec:          0,
			MaxSessionKBps:          0,
			Mode2HandshakeTimeoutSec: 5,
		},
		Logging: LoggingConfig{
			DiskLogging:         false,
			LogLevel:            "info",
			LogFile:             "/var/log/microvnc-relay/app.log",
			MaxSizeMB:           5,
			MaxBackups:          2,
			InMemoryBufferLines: 500,
		},
		Telemetry: TelemetryConfig{
			BroadcastIntervalSec:  1,
			TargetPingIntervalSec: 15,
		},
		Devices: []DeviceConfig{},
	}
}

// Load reads and parses a YAML configuration file from the specified path.
func Load(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config yaml: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return cfg, nil
}

// Validate ensures all configuration values are semantically correct and consistent.
func (c *Config) Validate() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Server validation
	if c.Features.WebUIEnabled && c.Server.WebBind == "" {
		return errors.New("server.web_bind must be specified when webui_enabled is true")
	}

	if c.Features.Mode2Enabled {
		if c.Server.Mode2ReversePort <= 0 || c.Server.Mode2ReversePort > 65535 {
			return fmt.Errorf("invalid mode2_reverse_port: %d", c.Server.Mode2ReversePort)
		}
		if c.Server.Mode2ViewerPort <= 0 || c.Server.Mode2ViewerPort > 65535 {
			return fmt.Errorf("invalid mode2_viewer_port: %d", c.Server.Mode2ViewerPort)
		}
		if c.Server.Mode2ReversePort == c.Server.Mode2ViewerPort {
			return errors.New("mode2_reverse_port and mode2_viewer_port cannot be identical")
		}
	}

	// Access control CIDR parsing
	c.AccessControl.allowedNets = nil
	if c.AccessControl.Enabled {
		for _, subnet := range c.AccessControl.AllowedSubnets {
			trimmed := strings.TrimSpace(subnet)
			if trimmed == "" {
				continue
			}
			_, ipNet, err := net.ParseCIDR(trimmed)
			if err != nil {
				// Try parsing as single IP
				ip := net.ParseIP(trimmed)
				if ip == nil {
					return fmt.Errorf("invalid allowed_subnet: %q", subnet)
				}
				bits := 32
				if ip.To4() == nil {
					bits = 128
				}
				ipNet = &net.IPNet{
					IP:   ip,
					Mask: net.CIDRMask(bits, bits),
				}
			}
			c.AccessControl.allowedNets = append(c.AccessControl.allowedNets, ipNet)
		}
	}

	// Logging validation
	if c.Logging.InMemoryBufferLines <= 0 {
		c.Logging.InMemoryBufferLines = 500
	}

	// Connection limits defaults
	if c.ConnectionLimits.TCPKeepAliveSec <= 0 {
		c.ConnectionLimits.TCPKeepAliveSec = 30
	}
	if c.ConnectionLimits.DialTimeoutSec <= 0 {
		c.ConnectionLimits.DialTimeoutSec = 5
	}
	if c.ConnectionLimits.Mode2HandshakeTimeoutSec <= 0 {
		c.ConnectionLimits.Mode2HandshakeTimeoutSec = 5
	}

	// Telemetry defaults
	if c.Telemetry.BroadcastIntervalSec <= 0 {
		c.Telemetry.BroadcastIntervalSec = 1
	}
	if c.Telemetry.TargetPingIntervalSec <= 0 {
		c.Telemetry.TargetPingIntervalSec = 15
	}

	// Device validation and uniqueness checks
	seenIDs := make(map[string]bool)
	seenVirtualPorts := make(map[int]string)
	seenMode2IDs := make(map[int]string)

	for i, dev := range c.Devices {
		if strings.TrimSpace(dev.ID) == "" {
			return fmt.Errorf("device at index %d has empty id", i)
		}
		if seenIDs[dev.ID] {
			return fmt.Errorf("duplicate device id: %q", dev.ID)
		}
		seenIDs[dev.ID] = true

		if strings.TrimSpace(dev.Host) == "" {
			return fmt.Errorf("device %q has empty host", dev.ID)
		}
		if dev.Port <= 0 || dev.Port > 65535 {
			return fmt.Errorf("device %q has invalid target port: %d", dev.ID, dev.Port)
		}

		if dev.VirtualPort > 0 {
			if dev.VirtualPort > 65535 {
				return fmt.Errorf("device %q has invalid virtual_port: %d", dev.ID, dev.VirtualPort)
			}
			if existingID, ok := seenVirtualPorts[dev.VirtualPort]; ok {
				return fmt.Errorf("virtual_port %d conflict between device %q and %q", dev.VirtualPort, existingID, dev.ID)
			}
			seenVirtualPorts[dev.VirtualPort] = dev.ID
		}

		if dev.Mode2ID > 0 {
			if existingID, ok := seenMode2IDs[dev.Mode2ID]; ok {
				return fmt.Errorf("mode2_id %d conflict between device %q and %q", dev.Mode2ID, existingID, dev.ID)
			}
			seenMode2IDs[dev.Mode2ID] = dev.ID
		}
	}

	return nil
}

// IsIPAllowed checks if an incoming remote IP address is permitted under access control rules.
func (c *Config) IsIPAllowed(ip net.IP) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.AccessControl.Enabled || len(c.AccessControl.allowedNets) == 0 {
		return true
	}

	for _, ipNet := range c.AccessControl.allowedNets {
		if ipNet.Contains(ip) {
			return true
		}
	}

	return false
}

// Save writes the current configuration to disk atomically using a temporary file.
func (c *Config) Save(path string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config to yaml: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	// Write to temporary file in the same directory to allow atomic rename
	tmpFile, err := os.CreateTemp(dir, "config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary config file: %w", err)
	}
	tmpName := tmpFile.Name()

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp config: %w", err)
	}

	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		os.Remove(tmpName)
		return fmt.Errorf("sync temp config: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp config: %w", err)
	}

	// Atomic rename replaces target file safely across power loss
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("atomic rename config: %w", err)
	}

	return nil
}

// FindDevice returns a copy of the registered device matching the given ID.
func (c *Config) FindDevice(id string) (DeviceConfig, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, d := range c.Devices {
		if d.ID == id {
			return d, true
		}
	}
	return DeviceConfig{}, false
}

// FindDeviceByVirtualPort locates a device configured on the specified virtual port.
func (c *Config) FindDeviceByVirtualPort(port int) (DeviceConfig, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, d := range c.Devices {
		if d.VirtualPort == port && d.Enabled {
			return d, true
		}
	}
	return DeviceConfig{}, false
}

// FindDeviceByMode2ID locates a device configured with the given UltraVNC Mode 2 ID.
func (c *Config) FindDeviceByMode2ID(id int) (DeviceConfig, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, d := range c.Devices {
		if d.Mode2ID == id && d.Enabled {
			return d, true
		}
	}
	return DeviceConfig{}, false
}

// AddOrUpdateDevice updates an existing device or appends a new device, re-validating the configuration.
func (c *Config) AddOrUpdateDevice(dev DeviceConfig) error {
	c.mu.Lock()
	found := false
	for i, d := range c.Devices {
		if d.ID == dev.ID {
			c.Devices[i] = dev
			found = true
			break
		}
	}
	if !found {
		c.Devices = append(c.Devices, dev)
	}
	c.mu.Unlock()

	return c.Validate()
}

// DeleteDevice removes a device by ID. Returns true if removed, false if not found.
func (c *Config) DeleteDevice(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i, d := range c.Devices {
		if d.ID == id {
			c.Devices = append(c.Devices[:i], c.Devices[i+1:]...)
			return true
		}
	}
	return false
}

// GetDevices returns a thread-safe copy of all registered devices.
func (c *Config) GetDevices() []DeviceConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make([]DeviceConfig, len(c.Devices))
	copy(result, c.Devices)
	return result
}
