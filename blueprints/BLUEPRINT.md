# μVNC Relay (MicroVNC Relay): Technical Architecture & Specification

## 1. Project Overview & Target Environment

`μVNC Relay` (executable: `microvnc-relay`) is an ultra-lightweight, zero-cache VNC (RFB) proxy, relay gateway, and visual management hub with an embedded WebUI, real-time bandwidth metering, and bidirectional latency inspection.

### Target Hardware & Resource Envelope
- **Tier 1 (Phase 1 Target)**: Raspberry Pi 2 (`armv7l`), Orange Pi Zero LTS, 32-bit Armbian TV boxes.
- **Tier 2 (Phase 2 Target)**: ARM64 (Raspberry Pi 3/4/5, Armbian Rockchip/Allwinner), x86-64 Linux, and optional Windows x64.
- **RAM Constraint**: Strictly **< 256MB system limit** (Process RSS budget: **< 35MB** under active streams).
- **Storage Protection**: **Zero data caching**. MicroSD and eMMC flash writes are avoided; logs remain in RAM unless explicitly enabled.

---

## 2. Phased Implementation Strategy

- **Phase 1: Proof of Concept (PoC)**
  - Target: **Debian 13 ("Trixie") on Raspberry Pi 2 (`armv7l`)**.
  - Goal: Run reliably as a standalone native binary / systemd service without major error, memory leaks, or SD card wear.
  - Deliverables:
    - Core Go RFB proxy (Mode 1 port forwarder & Mode 2 UltraVNC reverse ID matchmaker).
    - Stream forwarder using Linux kernel `splice(2)` zero-copy.
    - Two-hop latency engine (`TCP_INFO` kernel RTT + TCP SYN connect timing).
    - In-memory circular ring buffers for logs and metrics.
    - Minimal embedded WebUI dashboard for device mappings and live traffic meters.
    - Systemd autostart unit with memory cgroup ceiling (`MemoryMax=128M`).

- **Phase 2: Production Packaging & Expansion**
  - Multi-arch Docker images (`linux/arm/v7`, `linux/arm64`, `linux/amd64`).
  - Docker Compose setup with optional NetBird client sidecar.
  - Native `.deb` and `.rpm` packaging via `nfpm`.
  - Full visual network topology graph (interactive SVG/Canvas node map).
  - Embedded noVNC HTML5 in-browser viewer.

---

## 3. Technology Stack & Design Decisions

### Primary Language: **Go (Golang)**
- **Cross-Compilation**: Native `GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0` builds static binaries with zero glibc dependencies.
- **Kernel Zero-Copy**: Go's `io.Copy` automatically invokes Linux `splice(2)` for TCP-to-TCP streams, delivering near-wire-speed forwarding with < 2% CPU usage.
- **Single Executable**: WebUI assets are compiled directly into the binary via `//go:embed`.
- **Memory Ceiling**: Bounded with `GOMEMLIMIT=48MiB` and `GOGC=50`.

### Development Debug Bridge (Compile-Time Gated)
- To trace memory leaks and mock RFB streams during development without exposing attack surfaces or consuming memory in production:
  - `debug_dev.go` (`//go:build dev`): Mounts `net/http/pprof`, fault-injection hooks, and synthetic VNC targets.
  - `debug_prod.go` (`//go:build !dev`): Empty stubs optimized away by the compiler into zero instructions.

---

## 4. VNC / RFB Relay Engine Specifications

### Protocol Roles & Connection Quality
- **Quality is Client-Driven**: In RFB (RFC 6143), the viewer client dictates compression (`Tight`, `ZRLE`, etc.), color depth (`SetPixelFormat`), and JPEG quality (`SetEncodings`).
- **Transparent Relay**: The relay does not decode or re-compress video frames, preventing high CPU load on 900MHz Cortex-A7 cores.
- **Optional QoS**: WebUI allows optional session bandwidth caps (token bucket limiter) to protect congested links.

### Operational Modes
1. **Mode 1 (Port-Mapped Proxy)**:
   - Dedicated virtual ports (e.g., `:5910` ➔ `192.168.1.50:5900`) routed transparently.
2. **Mode 2 (UltraVNC ID Reverse Matchmaker)**:
   - Target machines behind NAT dial out to relay port `5500` with an ID token (`vncserver -connect <relay>:5500 -id 1234`).
   - Viewers connect to relay port `5901` specifying the matching ID.
   - Relay pairs and splices the two active sockets.
3. **Mode 3 (WebSocket Bridge for noVNC)**:
   - Upgrades browser HTTP connections to binary WebSockets (`/ws/vnc/{id}`) and bridges them to target TCP VNC sockets.

---

## 5. Modular Subsystem Toggles & Ultra-Lean Profiles

To accommodate the most resource-constrained edge hardware (e.g. Orange Pi Zero LTS with 256MB RAM or headless servers), all major subsystems can be toggled via `config.yaml`:

```yaml
features:
  webui_enabled: true             # Set false for Headless Pure Relay (~6-10MB RAM)
  topology_map_enabled: true      # Set false to disable Canvas/SVG map and use simple table
  novnc_enabled: true             # Set false to disable WebSocket-to-TCP bridge
  mode1_enabled: true             # Mode 1 direct port mapping
  mode2_enabled: true             # Mode 2 reverse ID listener (ports 5500 & 5901)
  latency_probes_enabled: true    # Background TCP SYN polling
  bandwidth_metering_enabled: true # Atomic RX/TX throughput counters
```

### Memory Footprint Profiles

| Operational Profile | Active Features | Idle RAM (RSS) | Active Load (4 Streams) | Use Case |
|---|---|---|---|---|
| **Full Stack** | WebUI + noVNC + Telemetry + Mode 1 & 2 | ~15 MB | ~25 MB – 32 MB | Default deployment, rich management |
| **Dashboard Only** | WebUI + Telemetry (noVNC disabled) | ~12 MB | ~18 MB – 22 MB | Native VNC viewers only (TigerVNC/RealVNC) |
| **Headless Pure Relay** | Mode 1 / 2 only (WebUI & noVNC OFF) | **~6 MB** | **~10 MB – 14 MB** | **Ultra-constrained 256MB SBCs & IoT gateways** |

---

## 6. State Management & Memory Durability

To ensure the daemon runs 24/7 indefinitely without memory growth or SD card failure:

| Component | Storage Mechanism | Retention / Lifecycle |
|---|---|---|
| **Device Config** | `/etc/microvnc-relay/config.yaml` | Persistent; read on boot, atomic write on WebUI edit only |
| **Active Sessions** | In-Memory Go Map | Ephemeral; freed upon socket close via `sync.Pool` |
| **Telemetry History** | Fixed-size Ring Buffer (1,440 slots) | ~46 KB fixed RAM; 24h rolling window (O(1) overwrite) |
| **Event Logs** | Fixed-size Ring Buffer (500 lines) | ~128 KB fixed RAM; zero disk writes unless enabled |
| **Framebuffer Data** | Kernel Pipe Buffers (`splice`) | **0 bytes disk caching; transient streaming only** |

---

## 7. Telemetry & Two-Hop Latency Engine

```
[Client / Viewer] ──(Hop 1: TCP_INFO RTT)──► [μVNC Relay] ──(Hop 2: TCP SYN Probe)──► [Target VNC]
```

1. **Hop 1 (Client ↔ Relay)**:
   - Read directly from the Linux kernel using `unix.GetsockoptTCPInfo(fd, SOL_TCP, TCP_INFO)`.
   - Returns microsecond-precision smoothed RTT without transmitting extra network packets.
2. **Hop 2 (Relay ↔ Target VNC Server)**:
   - Periodic non-intrusive TCP connection handshake (SYN ➔ SYN/ACK) executed every 15–30s.
   - Non-root compatible; works safely inside rootless containers and unprivileged systemd services.
3. **Bandwidth Metering**:
   - `MeteredConn` socket wrapper with atomic RX/TX byte counters pushed to WebUI every 1 second over WebSocket.

---

## 8. Service Lifecycle & Autostart

- **Native Linux (Systemd)**:
  - Unit `/etc/systemd/system/microvnc-relay.service` with `After=network-online.target netbird.service`.
  - Prevents early-boot binding crashes before interfaces (`wt0`, `eth0`) are up.
  - `Restart=always` with `RestartSec=5s` backoff.
- **Containerized (Docker - Phase 2)**:
  - Policy: `restart: unless-stopped`.
  - Resumes automatically across power cuts and host reboots while respecting manual administrative shutdowns.

---

## 9. Go Codebase Architecture & Directory Layout

```
microvnc-relay/
├── cmd/
│   └── microvnc-relay/
│       ├── main.go               # CLI entrypoint, signal handling, lifecycle
│       ├── debug_dev.go          # Development debug bridge (//go:build dev)
│       └── debug_prod.go         # Production no-op stubs (//go:build !dev)
├── internal/
│   ├── config/                   # YAML parsing, validation, and feature flags
│   ├── proxy/                    # RFB Mode 1, Mode 2 pool, zero-copy splice relay
│   ├── telemetry/                # TCP_INFO RTT, TCP SYN pingers, ring buffers
│   └── web/                      # REST API, auth session middleware, noVNC bridge
├── web/                          # WebUI frontend source (HTML/CSS/JS/Svelte)
│   └── dist/                     # Compiled SPA assets embedded into Go binary
├── blueprints/
│   └── BLUEPRINT.md
├── .gitignore
├── LICENSE
├── README.md
└── config.example.yaml
```

---

## 10. REST & WebSocket API Contract

All REST endpoints return JSON and are mounted under `/api/v1` (active when `features.webui_enabled: true`):

| Method | Endpoint | Description |
|---|---|---|
| `GET` | `/api/v1/devices` | List registered VNC targets with live status & latency |
| `POST` | `/api/v1/devices` | Add new target machine |
| `PUT` | `/api/v1/devices/{id}` | Update existing target configuration |
| `DELETE` | `/api/v1/devices/{id}` | Remove registered device |
| `POST` | `/api/v1/devices/{id}/test` | Trigger immediate TCP SYN latency test |
| `GET` | `/api/v1/sessions` | List active VNC client connections with RX/TX & RTT |
| `DELETE` | `/api/v1/sessions/{id}` | Force disconnect an active VNC session |
| `GET` | `/api/v1/metrics` | System snapshot (memory RSS, CPU %, cumulative bandwidth) |
| `GET` | `/api/v1/logs` | Fetch recent log lines from in-memory ring buffer |
| `POST` | `/api/v1/system/reset-counters`| Reset cumulative byte meters and 24h graph |
| `WS` | `/api/v1/ws/telemetry` | 1Hz streaming JSON telemetry to WebUI dashboard |
| `WS` | `/ws/vnc/{id}` | Binary RFB-over-WebSocket bridge for in-browser noVNC |
