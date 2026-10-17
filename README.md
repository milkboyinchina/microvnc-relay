<p align="center">
  <img src="assets/microvnc-relay-banner.jpg" alt="μVNC Relay - Old protocol. Zero copy. Pure speed." width="100%">
</p>

# μVNC Relay (MicroVNC Relay)

An ultra-lightweight TCP relay and VNC repeater with WebUI dashboard — VNC-first, generic TCP-capable. Zero-cache byte-stream forwarding with real-time bandwidth metering and bidirectional latency inspection.

Engineered specifically for **ARMv7** (Raspberry Pi 2, Orange Pi Zero LTS, 32-bit Armbian), **ARM64** (Raspberry Pi 3/4/5), and **x86-64**.

> [!NOTE]
> **Project Status: Alpha (`v0.1.0-alpha`) & Active Development**  
> `microvnc-relay` is currently in active alpha development. Core forwarding, Mode 1/2 proxying, and WebUI telemetry are fully functional and empirically verified on Raspberry Pi 2 hardware, but features and APIs are actively evolving. Feedback, bug reports, and contributions are welcome.

---

## Key Capabilities

- **Zero Data Caching & Flash Wear Protection**: All relayed streams (RFB or generic TCP) pass strictly through transient memory buffers; zero writes hit the MicroSD or eMMC flash storage.
- **Strict Memory Envelope**: Bounded memory footprint (< 35MB process RSS limit; **empirically measured at 6.6MB idle and 9.7MB under active multi-stream load on Raspberry Pi 2 ARMv7**), comfortably fitting inside 256MB/512MB RAM boards.
- **Modular Feature Toggles**: Disable unused subsystems in `config.yaml` (`webui_enabled`, `novnc_enabled`, `mode1_enabled`, `mode2_enabled`, `latency_probes_enabled`, `bandwidth_metering_enabled`) to eliminate background routines and minimize RAM.
- **Generic TCP Port Forwarding (Mode 1)**: Any TCP service (`RDP:3389`, `SSH:22`, `SMB:445/tcp`, `HTTP`, VNC) can be mapped via `virtual_port -> host:port`. Pure `io.Copy`/`splice(2)` byte-stream with no protocol decoding. TCP-only, no UDP.
- **Universal VNC Compatibility**: Supports TightVNC, TigerVNC, RealVNC, x11vnc, and UltraVNC (Mode 1 direct port mapping and Mode 2 reverse ID matchmaking). Mode 2 and noVNC bridge remain VNC-specific.
- **Two-Hop Latency Engine**:
  - *Client ↔ Relay*: Microsecond RTT extracted directly from Linux kernel `TCP_INFO`.
  - *Relay ↔ Target*: Active periodic TCP handshake latency probes.
- **Embedded WebUI & noVNC**: Self-contained web interface with device routing, real-time bandwidth gauges, and optional 1-click in-browser HTML5 remote control.
- **NetBird VPN Compatible**: Operates seamlessly over NetBird WireGuard mesh overlays (`wt0`), local LAN, or public networks.

## Why VNC in 2026?

Modern remote desktop solutions (RustDesk, WebRTC, Parsec) rely on continuous video streaming (H.264/HEVC). While effective for workstation-grade hardware, they break down in resource-constrained edge environments:

- **Software video encoders overwhelm low-power CPUs**: A 900MHz Cortex-A7 (Raspberry Pi 2) or 256MB RAM single-board computer lacks dedicated hardware video encoders. Running H.264/VP9 software encoding pins CPU cores at 100% and rapidly triggers out-of-memory (OOM) crashes.
- **VNC only transmits screen changes (dirty rectangles)**: By updating only changed pixel bounding boxes with lightweight tile compression (`Tight`/`ZRLE`), VNC servers run comfortably on ~15MB RAM with negligible CPU overhead.
- **Zero-copy kernel relaying (`splice(2)`)**: Because RFB is a byte-stream protocol, μVNC Relay pipes packets directly between kernel TCP socket buffers without decoding or re-compressing frames, keeping CPU load < 2%.
- **Modern networking solves VNC's historic weaknesses**: The classic downsides of VNC—unencrypted cleartext sessions and lack of NAT traversal—are cleanly resolved by pairing it with kernel-level WireGuard mesh overlays (**NetBird**). Transport encryption (ChaCha20-Poly1305) and NAT punch-through are offloaded to the kernel, delivering modern security with zero protocol bloat.

---

## Why μVNC Relay? Architecture & Tool Comparison

### Remote-desktop comparison

| Feature / Metric | **μVNC Relay + NetBird** | **RustDesk** | **TeamViewer / AnyDesk** | **RDP (`xrdp`) on Linux** |
|---|---|---|---|---|
| **Host RAM Footprint** | **< 10 MB** (Measured: 8.97 MB with 10 active streams on Pi 2) | ~100MB – 250MB | ~150MB – 300MB | ~80MB – 150MB |
| **CPU on ARMv7 SBCs** | **~2.1%** (Transparent relay) | High (Software video encode) | Moderate / High | Low / Moderate |
| **Open Source & License** | **Apache 2.0 (100% Free)** | AGPLv3 / Dual | ❌ Closed Source Proprietary | GPLv2 / MIT |
| **Commercial Nag Screens** | ❌ None (Self-hosted) | ❌ None | ⚠️ Timeouts & session blocks | ❌ None |
| **NAT Traversal** | WireGuard Mesh (NetBird) | STUN/TURN Rendezvous | Proprietary Cloud | ❌ Requires router port forward |
| **Transport Security** | Kernel ChaCha20-Poly1305 | TLS / NaCl | 🔒 Proprietary Encryption | ⚠️ Often self-signed / cleartext |
| **Browser Access (noVNC)**| **Built-in 1-Click HTML5** | ⚠️ Optional web client | ⚠️ Requires account | ❌ Needs desktop client |
| **Hardware Target** | **ARMv7, ARM64, x86-64** | Focus on modern ARM64/x64 | Deprecated/slow on ARMv7 | Standard Linux |

### Lightweight port-forwarding comparison (Mode 1)

Mode 1 is a generic TCP `virtual_port -> host:port` forwarder, comparable to:

| Tool | Type | WebUI / metering | VNC Mode-2 `ID:xxxx` | Notes vs μVNC Relay |
|---|---|---|---|---|
| `socat` / `rinetd` / `redir` | Single-shot TCP redirect | ❌ None | ❌ | Smaller, but one process per rule, no dashboard, no matchmaking |
| `iptables DNAT` / `nftables` | Kernel NAT | ❌ None | ❌ | Fastest, but no per-session metering, LAT/RTT, auth, or reverse mode |
| `HAProxy` / `nginx stream` | TCP load-balancer | ⚠️ Stats page only | ❌ | Full LB/TLS, heavier config, no VNC reverse ID |
| `frp` / `nps` | Reverse proxy + NAT traversal | ✅ Dashboard | ❌ | Needs client agent + central server; heavier than pure relay |
| `GOST` (GO Simple Tunnel) | Multi-protocol tunnel | ❌ / API only | ❌ | Closest in spirit (Go, TCP+UDP, chains), but no VNC-aware matchmaking or noVNC bridge |
| `ssh -L/-R` | Encrypted tunnel | ❌ None | ❌ | Ubiquitous, but per-tunnel process, TCP-over-TCP meltdown, no dashboard |
| **μVNC Relay Mode 1** | Static Go relay + WebUI | ✅ Bandwidth + `TCP_INFO` RTT | ✅ (via Mode 2) | **Positioning: lightweight port-forwarding app with dashboard, VNC-first.** TCP-only, no UDP, no LB/TLS termination — pair with NetBird/WireGuard for encryption/NAT. |

### Explicit non-goals (delegate outward)

To stay under 35MB RSS on ARMv7 and keep configuration to a single `config.yaml`:

* **No UDP relay** — TCP-only `splice(2)` byte-stream. UDP/RDP-UDP/DNS stays on WireGuard/NetBird natively.
* **No load balancing** — strictly `1 virtual_port -> 1 host:port`. Use `HAProxy`/`nginx stream` upstream if LB is needed.
* **No TLS termination** — no cert management or handshake crypto on the relay. NetBird ChaCha20-Poly1305 or a reverse proxy handles encryption.

### NetBird relay recipe (easier than `iptables`)

One NetBird node acts as relay, no `DNAT`/`conntrack`/rule-order/persistence to manage:

```yaml
server:
  web_bind: "100.64.0.1:8080"  # wt0 address only
  vnc_bind: "100.64.0.1"       # Mode 1 listeners on mesh only
access_control:
  enabled: true
  allowed_subnets: ["100.64.0.0/10"]
devices:
  - id: "lab-pc"
    host: "192.168.1.50"
    port: 5900        # any TCP: 5900 VNC, 3389 RDP, 22 SSH, 445/tcp SMB
    virtual_port: 5910  # clients use 100.64.0.1:5910
```

Add/remove mappings via WebUI (`POST /api/v1/devices`) or one YAML edit — no firewall reloads.

#### Config reload vs restart

* Devices-only (`devices:` list): hot-reloaded without dropping unchanged streams — via WebUI API or `sudo systemctl reload microvnc-relay` (`kill -HUP $MAINPID` → `Mode1Manager.Reload`).
* Everything else (`server.*`, `features.*`, `connection_limits.*` incl. `mode2_handshake_timeout_sec`, `logging`, `telemetry`, `security`, `access_control`): requires `sudo systemctl restart microvnc-relay`.
* Validate before restarting: `microvnc-relay -c /etc/microvnc-relay/config.yaml --check-config` (exit 0 = valid).
* Never edit non-device keys mid-load-test; the change won't apply until restart and invalidates stage comparison.

---

## 🔒 Security Architecture

Traditional VNC (RFB) protocol is inherently insecure when exposed to the public internet because it uses unencrypted cleartext streams and legacy 8-character DES authentication.

**μVNC Relay is architected to eliminate these risks by design:**

1. **Zero Public Internet Exposure**: μVNC Relay is intended to bind exclusively to private mesh network interfaces (such as **NetBird** WireGuard overlay `100.64.0.0/10`) or local physical subnets. **No open ports are required on your WAN router.**
2. **Cryptographic Transport Security**: When accessed through NetBird, all remote packets are wrapped inside kernel-level WireGuard encryption (ChaCha20-Poly1305), rendering eavesdropping and man-in-the-middle attacks impossible.
3. **Control Panel Protection**: WebUI management and the in-browser noVNC console are protected by session authentication with bcrypt-hashed credentials.
4. **Endpoint Defense**: Target machines running `x11vnc`, `tightvncserver`, or `winvnc` should continue enforcing their own strong VNC access passwords.

---

## ⚠️ Security Disclaimer & Limitation of Liability

```
THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE, AND NONINFRINGEMENT.

IN NO EVENT SHALL THE AUTHORS, MAINTAINERS, OR COPYRIGHT HOLDERS BE LIABLE
FOR ANY CLAIM, DAMAGES, OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
TORT, OR OTHERWISE, ARISING FROM, OUT OF, OR IN CONNECTION WITH THE SOFTWARE
OR THE USE, MISUSE, INABILITY TO USE, OR OTHER DEALINGS IN THE SOFTWARE.
```

- **User Responsibility**: You are solely responsible for the network configuration, interface binding, access controls, firewall policies, and password management of your deployments.
- **No Direct Internet Exposure**: Exposing unencrypted VNC ports or the μVNC Relay WebUI directly to the public internet without a secure encrypted overlay (e.g. NetBird, WireGuard, Tailscale) or reverse proxy TLS termination may expose your systems to unauthorized access, brute-force attacks, data interception, and remote machine control.
- **Financial & Operational Loss**: The authors and contributors shall NOT be held liable for any unauthorized access, security incidents, data breaches, financial loss, system downtime, damage to equipment, or compliance violations resulting from the use or misuse of this software.

---

## Implementation Roadmap

### Phase 1: Proof of Concept (Current Target)
- Target: **Debian 13 ("Trixie") on Raspberry Pi 2 (`armv7l`)**.
- Execution: Standalone Go static binary (`microvnc-relay`) and native systemd service (`microvnc-relay.service`).
- Verification: Long-running stability, zero memory leaks, and zero MicroSD card wear under active RFB streams.

### Phase 2: Packaging & Ecosystem Expansion
- Multi-arch Docker images (`linux/arm/v7`, `linux/arm64`, `linux/amd64`) with optional NetBird client sidecar.
- Native package generation (`.deb`, `.rpm`) via `nfpm`.
- Interactive SVG/Canvas visual network topology graph.
- Embedded noVNC HTML5 in-browser viewer.

---

## Technical Specifications

The full technical architecture, memory budgeting, protocol mechanics, and build tag configurations are detailed in:
- **[blueprints/BLUEPRINT.md](blueprints/BLUEPRINT.md)**

Project history and release notes:
- **[CHANGELOG.md](CHANGELOG.md)**

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

### Real-World Hardware Benchmarks (Raspberry Pi 2 `armv7l`)

Empirical measurements collected directly on physical hardware (Raspberry Pi 2 Model B, quad-core Cortex-A7 @ 900MHz, Debian Bookworm, NetBird WireGuard mesh overlay):

| Metric | Measured Physical Value | Target Constraint | Status |
|---|---|---|---|
| **Idle Process RSS** | **8.66 MB** | < 15.0 MB | **PASS** (Comfortably below target) |
| **Active Load RSS (2 Streams)** | **8.80 MB** (8,800 kB) | < 35.0 MB | **PASS** (< 26% of budget) |
| **Active Load RSS (5 Streams)** | **8.88 MB** (8,880 kB) | < 35.0 MB | **PASS** (< 26% of budget) |
| **Active Load RSS (10 Streams)** | **8.97 MB** (8,976 kB) | < 35.0 MB | **PASS** (+316 kB delta over 10 streams) |
| **Dynamic Heap/Stack (`RssAnon`)** | **2.61 MB** (2,680 kB) | Bounded via `GOMEMLIMIT` | Zero uncontrolled growth |
| **Clean Binary Code (`RssFile`)** | **6.15 MB** (6,296 kB) | Clean file-backed pages | Pure Go static executable |
| **Host RAM Availability** | **849 MB – 850 MB** free | > 500 MB safe margin | **PASS** (Zero OOM pressure) |
| **Teardown & FD Reclamation** | Returned to **11 FDs** post-test | Zero socket leak | **PASS** (Zero zombie sessions) |
| **Hop 2 TCP Dial RTT** | **1.61 ms – 3.21 ms** | Real-time SYN inspection | Microsecond precision |
| **Disk & Swap Activity** | **0 bytes** (Zero disk writes) | Zero flash wear | **PASS** (MicroSD protected) |

#### Resolution Independence: Why 1080p and 4K Displays Do Not Inflate RAM

Unlike remote desktop gateways (e.g. Apache Guacamole) that decode video frames into an uncompressed internal bitmap ($1920 \times 1080 \times 4\text{ bytes} \approx 8.3\text{ MB}$ per screen in memory):
- **Zero Framebuffer Caching**: `microvnc-relay` is a transparent byte-stream pipe. It never decodes RFB dirty rectangles or holds full screen frames in memory.
- **Fixed Bounded Chunking**: All streaming transfers pass through a reusable `sync.Pool` of **32 KB buffers** (or kernel `splice(2)` in unmetered mode). Each active connection holds at most ~64 KB of transient TCP data in flight regardless of whether the remote desktop resolution is $640 \times 480$, $1080\text{p}$, or $4\text{K}$.
- **What Scales with Resolution**: Screen resolution and graphical motion scale **network bandwidth** (e.g., 2–15 Mbps per active 1080p stream) and **CPU packet handling** (~10%–25% CPU on a 900MHz Cortex-A7 under heavy redraws), while process RAM remains bounded **under 12 MB**.


---

## License

This project is licensed under the **Apache License, Version 2.0**. See the [LICENSE](LICENSE) file for details.
