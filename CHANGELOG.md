# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Planned (Phase 1: Proof of Concept)
- Core Go RFB proxy engine supporting Mode 1 (port-mapped) and Mode 2 (UltraVNC reverse ID matchmaking).
- Linux kernel `splice(2)` zero-copy stream forwarding pipeline.
- Two-hop latency measurement engine (`TCP_INFO` microsecond kernel RTT and periodic TCP SYN probes).
- Atomic per-session bandwidth counters (RX/TX).
- Fixed-capacity circular ring buffers for in-memory event logs and telemetry history.
- Modular feature toggles supporting Headless Pure Relay mode (<10MB RAM).
- Embedded WebUI dashboard for device mappings and live traffic meters.
- Native systemd service unit with memory cgroup ceiling (`MemoryMax=128M`).

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
