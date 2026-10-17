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
- Milestone 5 embedded WebUI & REST / WebSocket Hub (`internal/web` and `web/dist`):
  - Self-contained, responsive vanilla SPA (`web/dist/index.html`, `style.css`, `app.js`) with zero CDN dependencies embedded into binary via `//go:embed`.
  - Pure-Go RFC 6455 WebSocket hub (`internal/web/ws_hub.go`) streaming 1Hz telemetry payloads (instantaneous and cumulative throughput, active streams, Hop 1 & Hop 2 RTTs, and OS process RSS) to connected dashboards without serialization overhead when idle.
  - REST API (`internal/web/api.go`): full device CRUD with atomic config save and live Mode 1 reloader invocation, immediate target test probes, active session listing and force-disconnect, system memory inspection, and event log ring buffer streaming.
  - Authentication manager (`internal/web/auth.go`) supporting bcrypt password hashes, automatic plaintext-to-bcrypt upgrade, and HTTP-only session cookies.
  - Integration test suite (`internal/web/web_test.go`) verifying static asset serving, auth protection, device CRUD, session termination, and WebSocket upgrades.
- Milestone 6 systemd service & deployment assets (`packaging/systemd`, `scripts`):
  - Systemd service unit (`packaging/systemd/microvnc-relay.service`) with `MemoryMax=128M` cgroup ceiling, `GOMEMLIMIT=48MiB`, `Restart=always`, and `After=network-online.target netbird.service`.
  - Multi-target static cross-compilation matrix verification across `linux/arm` (`GOARM=7`), `linux/arm64`, `linux/amd64`, and `windows/amd64`.
- WebUI Dashboard & Authentication Hardening:
  - Fixed missing `json:"..."` struct tags on `config.DeviceConfig` preventing target attributes (`virtual_port`, `enabled`, `mode2_id`) from serializing and binding properly.
  - Implemented First-Run WebUI Administrator Setup modal (`#setup-modal`) and `POST /api/v1/auth/setup` endpoint to securely configure credentials without plaintext defaults.
  - Added dedicated `auth.json` file storage (`0600` permissions) with automatic bcrypt hashing and simple file-deletion password reset capability (`rm auth.json`).
  - Added frontend fallback handling in `web/dist/app.js` and added `TestFirstRunAuthSetupAndReset` integration test.
- Stress Testing, Benchmarking & Engine Hardening:
  - Implemented standalone load harness (`cmd/stress-tester`) with automated circuit breaker / dead man's switch.
  - Added automated race-detected stress tests (`internal/proxy/stress_test.go`) covering concurrent forwarding, Mode 2 race conditions, and abrupt disconnections.
  - Empirically verified physical Raspberry Pi 2 hardware scaling across 2, 5, and 10 concurrent streams with RSS capped under 9.0 MB and zero file descriptor leaks.
  - Added `--check-config` CLI flag for dry-run configuration validation.
  - Added configurable `connection_limits.mode2_handshake_timeout_sec` (default 5s).





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
