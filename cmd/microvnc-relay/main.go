package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/milkboyinchina/microvnc-relay/internal/config"
	"github.com/milkboyinchina/microvnc-relay/internal/logger"
	"github.com/milkboyinchina/microvnc-relay/internal/proxy"
	"github.com/milkboyinchina/microvnc-relay/internal/telemetry"
	"github.com/milkboyinchina/microvnc-relay/internal/web"
)

// Version is the current application version (overridden by -ldflags at build time).
var Version = "0.1.0-alpha"

func main() {
	var (
		configPath  string
		showVersion bool
		checkConfig bool
		doReload    bool
	)

	flag.StringVar(&configPath, "config", "", "Path to YAML configuration file")
	flag.StringVar(&configPath, "c", "", "Path to YAML configuration file (shorthand)")
	flag.BoolVar(&showVersion, "version", false, "Print version and exit")
	flag.BoolVar(&showVersion, "v", false, "Print version and exit (shorthand)")
	flag.BoolVar(&checkConfig, "check-config", false, "Validate configuration file and exit (0 = valid, 1 = invalid)")
	flag.BoolVar(&doReload, "reload", false, "Send reload signal (SIGHUP) to running daemon and exit")
	flag.BoolVar(&doReload, "r", false, "Send reload signal (SIGHUP) to running daemon (shorthand)")
	flag.Parse()

	if showVersion {
		fmt.Printf("μVNC Relay (microvnc-relay) version %s (dev_build=%v)\n", Version, IsDevBuild)
		os.Exit(0)
	}

	if doReload {
		handleCLIReload()
		os.Exit(0)
	}

	printBanner()

	// Locate config file
	resolvedConfig := resolveConfigPath(configPath)

	cfg, err := config.Load(resolvedConfig)
	if err != nil {
		slog.Error("Failed to load configuration", "path", resolvedConfig, "error", err)
		os.Exit(1)
	}

	if checkConfig {
		fmt.Printf("Configuration %s is valid (%d device(s))\n", resolvedConfig, len(cfg.Devices))
		os.Exit(0)
	}

	// Initialize dual logger (stderr + fixed O(1) in-memory ring buffer)
	appLogger, err := logger.New(cfg.Logging, os.Stderr)
	if err != nil {
		slog.Error("Failed to initialize logger", "error", err)
		os.Exit(1)
	}
	defer appLogger.Close()
	appLogger.SetAsDefault()

	pidPath := writePIDFile()
	if pidPath != "" {
		defer os.Remove(pidPath)
		slog.Debug("Registered process PID file", "path", pidPath, "pid", os.Getpid())
	}

	slog.Info("Loaded configuration", "path", resolvedConfig)
	logConfigurationSummary(cfg)

	// Initialize traffic meter and 24h ring buffer
	meter := proxy.NewTrafficMeter(cfg.Features.BandwidthMeteringEnabled)
	stopMeter := make(chan struct{})
	if cfg.Features.BandwidthMeteringEnabled {
		go func() {
			ticker := time.NewTicker(1 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					meter.SampleTick()
				case <-stopMeter:
					return
				}
			}
		}()
	}

	// Start Mode 1 Port-Mapped Proxy Engine
	mode1Mgr := proxy.NewMode1Manager(cfg, meter, slog.Default())
	if err := mode1Mgr.Start(); err != nil {
		slog.Warn("[PORT COLLISION] One or more Mode 1 listeners failed to bind at startup", "error", err)
	}

	// Start Mode 2 UltraVNC ID Reverse Matchmaker
	mode2Mgr := proxy.NewMode2Manager(cfg, meter, slog.Default())
	if err := mode2Mgr.Start(); err != nil {
		slog.Error("[PORT COLLISION] Failed to start Mode 2 repeater listeners", "error", err)
		mode1Mgr.Stop()
		close(stopMeter)
		os.Exit(1)
	}

	// Start Two-Hop Latency Engine (Hop 2 TCP SYN pinger; Hop 1 TCP_INFO sampled in meter.SampleTick)
	pinger := telemetry.NewPinger(
		cfg,
		slog.Default(),
		meter.UpdateDeviceTargetRTT,
		mode2Mgr.HasPendingServer,
	)
	pinger.Start()

	// Start Embedded WebUI, REST API, and WebSocket Telemetry Hub
	webServer, err := web.NewServer(
		cfg,
		resolvedConfig,
		meter,
		pinger,
		appLogger,
		mode1Mgr.Reload,
		Version,
	)
	if err != nil {
		slog.Error("Failed to initialize WebUI server", "error", err)
		pinger.Stop()
		mode2Mgr.Stop()
		mode1Mgr.Stop()
		close(stopMeter)
		os.Exit(1)
	}
	webServer.SetBindErrorsGetter(mode1Mgr.GetBindErrors)

	if err := webServer.Start(); err != nil {
		slog.Error("[PORT COLLISION] Failed to start WebUI server listener", "error", err)
		pinger.Stop()
		mode2Mgr.Stop()
		mode1Mgr.Stop()
		close(stopMeter)
		os.Exit(1)
	}

	// Setup OS signal handling for clean teardown and SIGHUP config reload
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)

	slog.Info("μVNC Relay initialized successfully. Waiting for shutdown signal...")

	for {
		sig := <-sigChan
		if sig == syscall.SIGHUP {
			slog.Info("Received SIGHUP, reloading configuration", "path", resolvedConfig, "targets_path", cfg.TargetsPath())
			if reloaded, err := config.Load(resolvedConfig); err != nil {
				slog.Error("Failed to reload configuration on SIGHUP", "error", err)
			} else {
				// Partial hot-reload: reconcile Mode 1 device listeners and update in-memory device list.
				_ = cfg.SetDevices(reloaded.GetDevices())
				if err := mode1Mgr.Reload(cfg.GetDevices()); err != nil {
					slog.Warn("[PORT COLLISION] One or more Mode 1 listeners failed to bind during reload", "error", err)
				}
				if cfg.Features.LatencyProbesEnabled {
					go pinger.ProbeAll()
				}
				slog.Info("Targets hot-reloaded successfully", "count", len(cfg.GetDevices()))
			}
			continue
		}
		slog.Info("Received shutdown signal", "signal", sig.String())
		break
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = webServer.Stop(shutdownCtx)

	pinger.Stop()
	mode2Mgr.Stop()
	mode1Mgr.Stop()
	close(stopMeter)
	slog.Info("Shutting down μVNC Relay gracefully.")
}

func printBanner() {
	banner := `
 ╔══════════════════════════════════════════════════════════════════╗
 ║  __  ____      ____  _   _  ____   ____     _                    ║
 ║ |  \/  \ \    / /  \| \ | |/ ___| |  _ \___| | __ _ _   _        ║
 ║ | |\/| |\ \  / /| |) |  \| | |     | |_) / _ \ |/ _` + "`" + ` | | | |       ║
 ║ | |  | | \ \/ / |  _ <| |\  | |___  |  _ <  __/ | (_| | |_| |       ║
 ║ |_|  |_|  \__/  |_| \_\_| \_|\____| |_| \_\___|_|\__,_|\__, |       ║
 ║                                                        |___/     ║
 ║ μVNC Relay %-12s - Ultra-Lightweight VNC Proxy & Gateway ║
 ╚══════════════════════════════════════════════════════════════════╝
`
	fmt.Printf(banner, "v"+Version)
}

func resolveConfigPath(customPath string) string {
	if customPath != "" {
		return customPath
	}

	// Priority list of standard locations
	candidates := []string{
		"/etc/microvnc-relay/config.yaml",
		"./config.yaml",
		"./config.example.yaml",
	}

	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	return "./config.example.yaml"
}

func logConfigurationSummary(cfg *config.Config) {
	slog.Info("Subsystem Feature Flags",
		"webui", cfg.Features.WebUIEnabled,
		"topology_map", cfg.Features.TopologyMapEnabled,
		"novnc", cfg.Features.NoVNCEnabled,
		"mode1_proxy", cfg.Features.Mode1Enabled,
		"mode2_reverse", cfg.Features.Mode2Enabled,
		"latency_probes", cfg.Features.LatencyProbesEnabled,
		"bandwidth_meter", cfg.Features.BandwidthMeteringEnabled,
		"dev_debug_bridge", IsDevBuild,
	)

	slog.Info("Network Configuration",
		"web_bind", cfg.Server.WebBind,
		"vnc_bind", cfg.Server.VNCBind,
		"mode2_reverse_port", cfg.Server.Mode2ReversePort,
		"mode2_viewer_port", cfg.Server.Mode2ViewerPort,
		"registered_devices", len(cfg.Devices),
	)

	slog.Info("Storage & Limits",
		"disk_logging", cfg.Logging.DiskLogging,
		"in_memory_log_lines", cfg.Logging.InMemoryBufferLines,
		"access_control_enabled", cfg.AccessControl.Enabled,
		"tcp_keepalive_sec", cfg.ConnectionLimits.TCPKeepAliveSec,
		"dial_timeout_sec", cfg.ConnectionLimits.DialTimeoutSec,
	)
}

func handleCLIReload() {
	pid, path, err := findRunningPID()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: μVNC Relay daemon is not running (%v)\n", err)
		os.Exit(1)
	}

	if !isProcessAlive(pid) {
		fmt.Fprintf(os.Stderr, "Error: μVNC Relay PID %d from %s is not active\n", pid, path)
		os.Exit(1)
	}

	if err := sendReloadSignal(pid); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to send reload signal to PID %d: %v\n", pid, err)
		os.Exit(1)
	}

	fmt.Printf("✓ Reload signal (SIGHUP) sent to μVNC Relay (PID %d). Targets reloaded successfully.\n", pid)
}

func findRunningPID() (int, string, error) {
	candidates := []string{
		"/run/microvnc-relay.pid",
		"/var/run/microvnc-relay.pid",
		"/tmp/microvnc-relay.pid",
	}

	for _, p := range candidates {
		data, err := os.ReadFile(p)
		if err == nil {
			var pid int
			if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid); err == nil && pid > 0 {
				return pid, p, nil
			}
		}
	}
	return 0, "", errors.New("PID file not found")
}

func writePIDFile() string {
	candidates := []string{
		"/run/microvnc-relay.pid",
		"/var/run/microvnc-relay.pid",
		"/tmp/microvnc-relay.pid",
	}

	pidStr := fmt.Sprintf("%d\n", os.Getpid())
	for _, p := range candidates {
		if err := os.WriteFile(p, []byte(pidStr), 0644); err == nil {
			return p
		}
	}
	return ""
}
