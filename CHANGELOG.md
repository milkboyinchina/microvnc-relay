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
