# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Milestone 1 scaffolding: Go module `github.com/milkboyinchina/microvnc-relay` with pure-Go dependencies (`gopkg.in/yaml.v3`, `golang.org/x/crypto`, `golang.org/x/sys`).
- Configuration package (`internal/config`) with full YAML parsing, CIDR subnet access control, connection limits, and atomic save via temporary file replacement.
- Comprehensive unit test suite in `internal/config/config_test.go` verifying access control, port conflict detection, and atomic persistence.
- Compile-time debug bridge stubs (`cmd/microvnc-relay/debug_dev.go` vs `debug_prod.go`) isolating `pprof` diagnostic routes to `-tags dev` builds.
- CLI entrypoint (`cmd/microvnc-relay/main.go`) with ASCII startup banner, automatic config resolution, and OS signal handling.
- Milestone 2 ephemeral state & telemetry foundation:
  - Generic thread-safe fixed-capacity circular ring buffer (`internal/telemetry/ringbuffer.go`) with $O(1)$ overwrite and zero unbounded slice growth.
  - Dual structured `slog.Handler` (`internal/logger/logger.go`) writing to `stderr` and an in-memory ring buffer (default 500 lines) with optional size-rotated disk logging (`disk_logging: false` by default).
  - Atomic byte metering, active session registry, and 1,440-slot (24h) bandwidth history ring buffer (`internal/proxy/meter.go`).
- Milestone 3 core RFB proxy engine (`internal/proxy`):
  - Bidirectional stream forwarder (`internal/proxy/forwarder.go`) with deadlock-free socket teardown, Linux kernel `splice(2)` zero-copy in unmetered headless mode, and zero-allocation 32KB `sync.Pool` buffers for real-time byte metering, QoS bandwidth caps (`max_session_kbps`), and idle timeout enforcement (`idle_timeout_sec`).
  - Mode 1 Port-Mapped Proxy manager (`internal/proxy/mode1.go`) with dynamic virtual port binding, live `Reload(devices)` without dropping unchanged listeners, CIDR allowlist enforcement, and initial TCP dial RTT measurement.
  - Mode 2 UltraVNC ID reverse matchmaker (`internal/proxy/mode2.go`) on `:5500` (reverse server pool) and `:5901` (viewer port with `"RFB 000.000\n"` greeting and 250-byte `"ID:xxxx"` handshake parsing).
  - End-to-end integration tests (`internal/proxy/proxy_test.go`) covering Mode 1 forwarding, live listener reload, CIDR rejection, Mode 2 UltraVNC matchmaking, QoS pacing, and idle timeout teardown.
- Milestone 4 two-hop latency engine (`internal/telemetry`):
  - Hop 1 (Client $\leftrightarrow$ Relay) zero-packet Linux kernel `TCP_INFO` RTT inspector (`internal/telemetry/tcpinfo_linux.go`) via `unix.GetsockoptTCPInfo(fd, SOL_TCP, TCP_INFO)` with cross-platform fallback stub (`internal/telemetry/tcpinfo_other.go`).
  - Hop 2 (Relay $\leftrightarrow$ Target) non-intrusive TCP SYN handshake pinger (`internal/telemetry/pinger.go`) with configurable polling interval (`target_ping_interval_sec`), on-demand single-device probe support, Mode 2 waiting-server awareness, and bounded status map pruning.
  - Unit tests (`internal/telemetry/pinger_test.go`) verifying kernel `TCP_INFO` extraction, online/offline target detection, map pruning, and `latency_probes_enabled: false` feature gating.


## [0.1.0-alpha] - 2026-10-16

### Added
- Comprehensive architecture specification and blueprints in `blueprints/BLUEPRINT.md`.
- Complete reference configuration template in `config.example.yaml` featuring:
  - Granular feature toggles (`webui_enabled`, `topology_map_enabled`, `novnc_enabled`, `mode1_enabled`, `mode2_enabled`, `latency_probes_enabled`, `bandwidth_metering_enabled`).
  - CIDR subnet allowlist access control.
  - Connection timeouts (TCP keepalive, dial timeout, idle timeout) and QoS bandwidth capping.
  - Ephemeral memory logging controls for flash wearout protection.
- Apache License, Version 2.0.
- Project `README.md` with capabilities overview, hardware target matrix, competitive comparison table (vs. RustDesk, TeamViewer, RDP), and security architecture.
- Prominent Security Disclaimer and Limitation of Liability in `README.md`.
- Build and ignore rules in `.gitignore` for Go, WebUI, and multi-architecture binary artifacts (`microvnc-relay*`, `uvnc-relay*`).
