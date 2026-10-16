# μVNC Relay (MicroVNC Relay)

An ultra-lightweight, zero-cache VNC (RFB) proxy, relay gateway, and visual management hub with real-time bandwidth metering and bidirectional latency inspection.

Engineered specifically for **ARMv7** (Raspberry Pi 2, Orange Pi Zero LTS, 32-bit Armbian), **ARM64** (Raspberry Pi 3/4/5), and **x86-64**.

---

## Key Capabilities

- **Zero Data Caching & Flash Wear Protection**: All RFB framebuffer updates stream strictly through transient memory buffers; zero writes hit the MicroSD or eMMC flash storage.
- **Strict Memory Envelope**: Bounded memory footprint (< 35MB process RSS in full WebUI mode, dropping to **~6MB – 10MB in Headless Pure Relay mode**), comfortably fitting inside 256MB/512MB RAM boards.
- **Modular Feature Toggles**: Disable unused subsystems in `config.yaml` (`webui_enabled`, `novnc_enabled`, `mode1_enabled`, `mode2_enabled`, `latency_probes_enabled`, `bandwidth_metering_enabled`) to eliminate background routines and minimize RAM.
- **Universal VNC Compatibility**: Supports TightVNC, TigerVNC, RealVNC, x11vnc, and UltraVNC (Mode 1 direct port mapping and Mode 2 reverse ID matchmaking).
- **Two-Hop Latency Engine**:
  - *Client ↔ Relay*: Microsecond RTT extracted directly from Linux kernel `TCP_INFO`.
  - *Relay ↔ Target*: Active periodic TCP handshake latency probes.
- **Embedded WebUI & noVNC**: Self-contained web interface with device routing, real-time bandwidth gauges, and optional 1-click in-browser HTML5 remote control.
- **NetBird VPN Compatible**: Operates seamlessly over NetBird WireGuard mesh overlays (`wt0`), local LAN, or public networks.

---

## Why μVNC Relay? Architecture & Tool Comparison

| Feature / Metric | **μVNC Relay + NetBird** | **RustDesk** | **TeamViewer / AnyDesk** | **RDP (`xrdp`) on Linux** |
|---|---|---|---|---|
| **Host RAM Footprint** | **< 25 MB** (Ultra-lean) | ~100MB – 250MB | ~150MB – 300MB | ~80MB – 150MB |
| **CPU on ARMv7 SBCs** | **< 2%** (Transparent relay) | High (Software video encode) | Moderate / High | Low / Moderate |
| **Open Source & License** | **Apache 2.0 (100% Free)** | AGPLv3 / Dual | ❌ Closed Source Proprietary | GPLv2 / MIT |
| **Commercial Nag Screens** | ❌ None (Self-hosted) | ❌ None | ⚠️ Timeouts & session blocks | ❌ None |
| **NAT Traversal** | WireGuard Mesh (NetBird) | STUN/TURN Rendezvous | Proprietary Cloud | ❌ Requires router port forward |
| **Transport Security** | Kernel ChaCha20-Poly1305 | TLS / NaCl | 🔒 Proprietary Encryption | ⚠️ Often self-signed / cleartext |
| **Browser Access (noVNC)**| **Built-in 1-Click HTML5** | ⚠️ Optional web client | ⚠️ Requires account | ❌ Needs desktop client |
| **Hardware Target** | **ARMv7, ARM64, x86-64** | Focus on modern ARM64/x64 | Deprecated/slow on ARMv7 | Standard Linux |

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
