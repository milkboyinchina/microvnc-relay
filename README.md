# μVNC Relay (MicroVNC Relay)

An ultra-lightweight, zero-cache VNC (RFB) proxy, relay gateway, and visual management hub with real-time bandwidth metering and bidirectional latency inspection.

Engineered specifically for **ARMv7** (Raspberry Pi 2, Orange Pi Zero LTS, 32-bit Armbian), **ARM64** (Raspberry Pi 3/4/5), and **x86-64**.

---

## Key Capabilities

- **Zero Data Caching & Flash Wear Protection**: All RFB framebuffer updates stream strictly through transient memory buffers; zero writes hit the MicroSD or eMMC flash storage.
- **Strict Memory Envelope**: Bounded memory footprint (< 35MB process RSS), comfortably fitting inside 256MB/512MB RAM boards.
- **Universal VNC Compatibility**: Supports TightVNC, TigerVNC, RealVNC, x11vnc, and UltraVNC (Mode 1 direct port mapping and Mode 2 reverse ID matchmaking).
- **Two-Hop Latency Engine**:
  - *Client ↔ Relay*: Microsecond RTT extracted directly from Linux kernel `TCP_INFO`.
  - *Relay ↔ Target*: Active periodic TCP handshake latency probes.
- **Embedded WebUI**: Self-contained web interface with device routing, real-time bandwidth gauges, and an in-memory ephemeral log inspector.
- **NetBird VPN Compatible**: Operates seamlessly over NetBird WireGuard mesh overlays (`wt0`), local LAN, or public networks.

---

## Implementation Roadmap

### Phase 1: Proof of Concept (Current Target)
- Target: **Debian 13 ("Trixie") on Raspberry Pi 2 (`armv7l`)**.
- Execution: Standalone Go static binary (`microvnc-relay`) and native systemd service (`microvnc-relay.service`).
- Verification: Long-running stability, zero memory leaks, and zero MicroSD card wear under active RFB streams.

### Phase 2: Packaging & Ecosystem Expansion
- Multi-arch Docker images (`linux/arm/v7`, `linux/arm64`, `linux/amd64`) with optional NetBird client sidecar.
- Native package generation (`.deb`, `.rpm`) and Flatpak manifest (`org.microvnc.Relay`).
- Interactive SVG/Canvas visual network topology graph.
- Embedded noVNC HTML5 in-browser viewer.

---

## Technical Specifications

The full technical architecture, memory budgeting, protocol mechanics, and build tag configurations are detailed in:
- **[blueprints/BLUEPRINT.md](blueprints/BLUEPRINT.md)**

Sample configuration:
- **[config.example.yaml](config.example.yaml)**

---

## Hardware Targets

| Platform | Architecture | System RAM | Process Budget |
|---|---|---|---|
| **Raspberry Pi 2 / Orange Pi Zero LTS** | ARMv7 (`armv7l`) | 256MB – 512MB | **< 30MB** |
| **Armbian TV Boxes** | ARMv7 / ARM64 | 512MB – 1GB | **< 35MB** |
| **Raspberry Pi 3/4/5** | ARM64 (`aarch64`) | 1GB – 8GB | **< 35MB** |
| **Linux Servers & Workstations** | x86-64 (`amd64`) | 1GB+ | **< 40MB** |

---

## License

This project is licensed under the **Apache License, Version 2.0**. See the [LICENSE](LICENSE) file for details.

