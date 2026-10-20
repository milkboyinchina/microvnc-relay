# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Gate 0 — Phase 1 Audit Hardening

> **Gate 0 (Pi 2) complete**: 24h soak passed (2,920 stages, max RSS 10,084 KB, 0 heartbeat failures, no circuit breaker). Rated-load RSS (32 streams) measured at 8,092 KB. Systemd unit tested (active, MemoryHigh=32M/MemoryMax=48M; memory cgroup not mounted on this Pi OS image — limits inert). Pi 1/Zero (ARMv6) soak deferred pending hardware. The code-level, race, cross-build, and Docker data-plane evidence is in this tree.

#### Security
- Token-only first-run setup: a one-time token is printed to stdout/journal; `auth.json` is created with `O_EXCL` and setup is hard-disabled once it exists. No default credentials.
- Bcrypt work is serialized by a one-slot semaphore; concurrent compares get `429` instead of pinning goroutines.
- Login limiter keys IPv6 by `/64`, normalizes IPv4-mapped addresses, bounds the map at 256 entries, and evicts the least-offending unlocked entry.
- Active sessions are capped at 64 with oldest-eviction; WebSocket clients are capped at 8 with oldest-eviction (`1013` close).
- Same-host WebSocket `Origin` validation, `Sec-WebSocket-Version: 13`, and a 4KB frame cap.
- Systemd unit can write `auth.json`, `targets.yaml`, and the PID file under `ProtectSystem=full`.

#### Added
- Transactional device CRUD: validate → atomic persist → commit; failed writes return `500` and roll back in-memory state.
- SIGHUP applies devices, access-control CIDRs, connection limits, and the target probe interval; restart-only changes warn explicitly.
- Documented `server.targets_file` resolution with legacy top-level fallback.
- One shared 4-slot probe scheduler with `TriggerSweep`, per-host jitter (±20%), and `SO_LINGER=0` probes.
- Keepalive (15s) and Linux `TCP_USER_TIMEOUT=30s` on every accepted/dialed socket; EOF-driven `CloseWrite` with a bounded drain.
- Authoritative session registry: stop admission, disconnect all, drain within a bounded grace period, report stragglers.
- Hard admission caps: 32 global / 8 per device / 64 pending handshakes / 8 WebSocket clients; overload telemetry.
- Mode 2: Accept-time handshake deadlines, bounded pending-viewer queue with timeout, duplicate-ID takeover with `MSG_PEEK`, and single-owner `ack`/`done` pairing.
- Linux splice-metered forwarding with per-direction 16KB pipes and live byte counters from splice return values; QoS remains the only userspace pacing path.
- Splice engagement/fallback metrics, admission metrics, and WebSocket rejection metrics.
- CI: cross-build matrix (ARMv6/ARMv7/ARM64/AMD64/Windows), amd64 race, QEMU ARM tests, `govulncheck`, `goleak`, fuzz targets, dev builds, and a Docker compose integration job.
- Dev-only Docker harness: Hub → Site DMZ → Floor Pi topology, synthetic VNC targets, RST/drop fault endpoints, direct/chained/abrupt-disconnect/RST/drop/netem/loop scenarios.
- Installer applies `net.ipv4.tcp_mtu_probing=1`, checks the memory cgroup, and prints the first-run token command.
- `scripts/prepare-cloned-image.sh` resets machine-id and removes copied credentials/identity state.
- `scripts/soak.sh` and `scripts/verify-pi2.sh` collect RSS, cgroup memory, Slab, FDs, goroutines, disk writes, and heartbeat data.

#### Changed
- Go directive bumped to `1.26.9` after `govulncheck` found callable standard-library vulnerabilities in 1.26.0.
- Default `tcp_keepalive_sec` is now 15; added `tcp_user_timeout_sec` (30) and `half_close_timeout_sec` (5).
- `max_session_kbps` is documented as the only remaining non-splice forwarding mode.
- `blueprints/BLUEPRINT.md` Gate 0 items are marked complete; chain-control-plane harness scenarios are explicitly deferred to Phase 2a/b/c.

#### Fixed
- `server.targets_file` was documented but silently ignored; now resolved and tested.
- `VerifyCredentials` held the auth write lock during bcrypt, blocking session validation.
- CRUD persistence failures returned `5xx` but left ghost devices in memory.
- Probe goroutines created by CRUD handlers bypassed the scheduler and leaked on shutdown.
- Pinger sweeps outlived `Stop()` and could overlap.
- Mode 1/2 accept loops could call `WaitGroup.Add` after shutdown began waiting.
- Mode 2 had a prune-vs-pair socket race and no viewer wait/duplicate-ID policy.
- Systemd `ProtectSystem=full` made `/etc` read-only, breaking credential and target persistence.
- `PrivateTmp` hid the `/tmp` PID fallback; the runtime directory is now `/run/microvnc-relay`.
- A flaky Mode 1 byte-counter test snapshotted before the forwarding goroutine finished.

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
