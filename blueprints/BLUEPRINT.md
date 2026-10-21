# μVNC Relay (MicroVNC Relay): Technical Architecture & Specification

> **Official Repository**: [https://github.com/milkboyinchina/microvnc-relay](https://github.com/milkboyinchina/microvnc-relay)  
> **Author / Maintainer**: milkboyinchina  
> **License**: Apache 2.0  
> **Canonical Specification**: Technical Architecture, Zero-Cache Edge Relay Engine, & Multi-Hop Network Design  

## 1. Project Overview & Target Environment

`μVNC Relay` (executable: `microvnc-relay`) is an ultra-lightweight, zero-cache VNC (RFB) proxy, relay gateway, and visual management hub with an embedded WebUI, real-time bandwidth metering, and bidirectional latency inspection.

### Target Hardware & Resource Envelope
- **Tier 1 (Primary Edge Target)**: Raspberry Pi 1 / Pi Zero (`armv6l` / `armhf`, `GOARM=6`), Raspberry Pi 2 (`armv7l`, `GOARM=7`), Orange Pi Zero LTS, 32-bit Armbian TV boxes.
- **Tier 2 (Secondary Target)**: ARM64 (Raspberry Pi 3/4/5, Armbian Rockchip/Allwinner), x86-64 Linux, and Windows x64.
- **RAM Constraint**: Strictly **< 256MB system limit** (Process RSS budget: **< 35MB at rated load** — ≤32 concurrent streams, ≤8 WebSocket clients — plus a documented kernel-memory envelope: `F_SETPIPE_SZ`-shrunk 16KB/pipe splice buffers + socket buffers; empirically measured at **~8.97 MB** across 10 concurrent streams on Raspberry Pi 2.
  - **Gate 0 (Pi 2) — code-complete, evidence not closed**: CI is green on `f9a6a65` (run 39535071455), including amd64 race/vet, QEMU arm32, GOARM=6 QEMU, cross-builds, govulncheck (Go 1.26.10), fuzz, and the dev compose harness. The committed 24h soak artifact reports 2,920 stages, max RSS **10,084 KB**, and no circuit breaker, but its producing runner is not yet reconciled with `scripts/soak.sh`. Rated-load (32 streams × 64 KB + 8 WebSocket clients) RSS and the systemd `MemoryHigh=32M`/`MemoryMax=48M` chain remain **unverified** — no supporting artifact exists, and the deployed unit used `GOMEMLIMIT=48MiB`/`MemoryMax=128M` with the memory cgroup unavailable.
  - **Gate 0 (Pi 1/Zero ARMv6)**: **deferred** — pending hardware provisioning. Cross-compile (`GOARM=6`) verified in CI, the QEMU `linux/arm/v7` + `GOARM=6` suite passes, and the binary starts on a Pi 2 (ARMv7) without `SIGILL`. Physical Pi 1/Zero soak and 256MB envelope measurement are not yet run; expected to work on Pi 1 hardware.
- **Storage Protection**: **Zero data caching**. MicroSD and eMMC flash writes are avoided; logs and telemetry remain in fixed-size RAM ring buffers unless explicitly enabled.

---

## 2. Phased Implementation Roadmap & TODOs

The project is organized into **4 incremental phases plus a Gate 0 hardening pass**, sorted by technical dependency and operational value:

> **Provenance (17 Oct 2026)**: The Gate 0 → 2a → 2b → 2c structure and the hardened checklist items below derive from the consolidated five-reviewer AI audit (`blueprints/ai-reponses/consolidated-response.md`). The immediate-next-hop routing-vs-metadata separation was unanimously retained; the zero-credential subnet-ACL trust model, uncapped hop depth, and the binary refused/timeout error taxonomy were rejected and replaced.

### Phase 1: Core Proxy Engine, Security Hardening & Edge PoC (COMPLETED)
- **Target**: Debian 12/13 on Raspberry Pi 1/Zero (`armv6l` / `armhf`) & Raspberry Pi 2 (`armv7l`).
- **Goal**: Run reliably 24/7 as a standalone static binary / `systemd` service with zero memory leaks, zero flash wear, and full port-collision / brute-force safety.
- **Phase 1 Checklist**:
  - [x] Core Go RFB proxy: Mode 1 generic TCP port forwarder & Mode 2 UltraVNC reverse ID matchmaker (`:5500` / `:5901`).
  - [x] Stream forwarder using Linux kernel `splice(2)` zero-copy (`spliceMetered`, 16 KB chunks/pipes) with pooled 32 KB `sync.Pool` fallback buffers and optional token-bucket QoS rate limiting. `MeteredConn` remains a test/utility wrapper, not the production fast path.
  - [x] Mode 2 waiting server pool cap (`maxPendingServers = 64`) and non-blocking `MSG_PEEK` dead-socket pruning (`isSocketAlive`).
  - [x] Two-hop latency engine: Hop 1 `TCP_INFO` kernel RTT (`*_linux.go` / `*_other.go`) + Hop 2 bounded concurrent TCP SYN connect pinger (4-worker semaphore).
  - [x] Dual-file configuration (`config.yaml` for static daemon settings, `targets.yaml` for dynamic targets) with atomic writes and backward compatibility.
  - [x] Live CLI hot-reload (`microvnc-relay --reload` / `-r` via PID file + `SIGHUP`) and `--check-config` validator.
  - [x] Static cross-service port collision validation (`web_bind`, `mode2` ports, duplicate `virtual_port`) + runtime OS `EADDRINUSE` tracking (`bind_errors`) across CLI, `systemd`, and WebUI.
  - [x] First-run WebUI admin credential setup (`auth.json` with `bcrypt`, **no default credentials**) and bounded in-memory brute-force rate limiter (`LoginRateLimiter`: 5 failures/5m ➔ 15m `HTTP 429` lockout; progressive-delay attempts are rejected immediately with `429` + `Retry-After` — no handler sleeps).
  - [x] Fixed-capacity circular ring buffers for event logs (500 lines) and 24h telemetry history (1,440 slots).
  - [x] Embedded dark-mode WebUI dashboard (~47 KB raw / ~11 KB gzipped vanilla JS/CSS) with 1Hz WebSocket telemetry, device CRUD, and live toast notifications.
  - [x] Systemd unit (`GOMEMLIMIT=24MiB` + `MemoryHigh=32M`/`MemoryMax=48M` cgroup chain — see §3 & Gate 0) and multi-arch auto-detecting installer (`scripts/install-systemd.sh`).
  - [x] Empirical stress-test verification on physical Raspberry Pi 2 hardware (`cmd/stress-tester`).

> **Audit note (17 Oct 2026)**: Four verified code defects and several lifecycle gaps in shipped Phase 1 features are tracked in **Gate 0** below — Gate 0 must land before any Phase 2 control-plane code.

---

### Gate 0: Phase 1 Audit Hardening (BEFORE PHASE 2)
- **Target**: Close all verified code-level defects and lifecycle gaps in already-shipped Phase 1 features **before any Phase 2 control-plane code is written**.
- **Provenance**: P0 items from the consolidated five-reviewer audit (`blueprints/ai-reponses/consolidated-response.md`, 17 Oct 2026). Four items were fixed same-day (marked `[x]` below); the remainder are tracked here.
- **Gate 0 Checklist**:
  - [x] **Auth Bootstrap — no default credentials** *(fixed 17 Oct 2026)*: removed the default `admin`/bcrypt hash from shipped defaults — a fresh install must complete first-run setup (`auth.json`) before any session can be issued.
  - [x] **Auth Bootstrap (remainder)**: bind first-run setup to localhost or a one-time setup token printed to stdout/journal; hard-403 once `auth.json` exists; atomic `O_EXCL` + mutex on setup (closes the concurrent first-run race); define the password-reset procedure (deleting `auth.json` currently re-opens setup).
  - [x] **Login Limiter (remainder)**: bcrypt semaphore (max 1 concurrent compare — one cost-10 compare ≈ 1s CPU on a 700MHz ARMv6; concurrent compares are a single-core CPU DoS); key IPv6 sources by /64; bounded eviction policy (evict least-offending unlocked entries).
  - [x] **Session Cookie `Secure` Flag** *(fixed 17 Oct 2026)*: `security.secure_cookies` config sets the `Secure` attribute alongside `HttpOnly`/`SameSite` (enable when serving over HTTPS).
  - [x] **Transactional CRUD Responses** *(fixed 17 Oct 2026)*: device add/update/delete handlers return `500` when `targets.yaml` persistence fails — never log-then-`2xx`.
  - [x] **No-Sleep Progressive Delay** *(fixed 17 Oct 2026)*: progressive-delay login attempts are rejected immediately with `429` + `Retry-After` — a sleeping handler pins a goroutine and socket per attempt (self-inflicted slowloris).
  - [x] **Probe Scheduler Unification (`internal/telemetry/pinger.go`)**: route `POST /api/v1/devices/{id}/test` and create/update validation probes through the shared bounded 4-worker scheduler — no ad-hoc probe goroutines bypassing resource bounds.
  - [x] **Mode 2 Matchmaker Hardening (`internal/proxy/mode2.go`)**:
    - [x] Enforce the 5s handshake read deadline on `:5500`/`:5901` from `Accept()` until the ID token is parsed/matched (anti-slowloris).
    - [x] Cap and timeout the pending-viewer queue (symmetric with `maxPendingServers = 64`).
    - [x] Duplicate-ID policy: `MSG_PEEK` liveness check → evict-older + takeover warning (NAT rebinding orphans sockets before keepalive fires).
    - [x] Single-owner pairing handoff (channel handoff or per-entry state machine) to close the prune-vs-pair race.
  - [x] **Socket Lifecycle (`internal/proxy/`)**:
    - [x] `SetKeepAlive(true)` + 15s period and `TCP_USER_TIMEOUT=30s` on every accepted and dialed connection, both directions, every hop (dead peers currently leave goroutines, FDs, and sessions forever).
    - [x] `CloseWrite` half-close handling with a bounded wait on the other direction.
    - [x] Authoritative active-session registry: stop admitting during shutdown, close all active sockets, wait only a bounded grace period, report sessions still pending.
  - [x] **Admission Control (all modes)**: global + per-target session caps, accepted-but-unauthenticated socket cap, WebSocket client cap (8, oldest-eviction) — defined behavior and explicit overload state at each limit. (`sync.Pool` is allocation reuse, not a memory cap — 500 streams × 64KiB ≈ 31MB of buffers alone.)
  - [x] **Metered Zero-Copy Path (`internal/proxy/proxy_linux.go` + `proxy_other.go`)**:
    - [x] Meter byte counts via `splice(2)` return values (kernel-side) without a userspace copy.
    - [x] Shrink kernel pipe buffers via `F_SETPIPE_SZ=16KB` on constrained boards (−75% kernel slab per stream).
    - [x] Document the trade-off: token-bucket QoS pacing inherently forces the userspace copy/pacing path; it is the only remaining non-splice mode.
  - [x] **32-bit ARM Safety (`internal/`)**: typed `atomic.Uint64` counters (auto-aligned — misaligned `atomic.AddUint64` panics on `GOARCH=arm`); `GOARCH=arm` build + test in CI.
  - [x] **Probe Hygiene (`internal/telemetry/pinger.go`)**: `SO_LINGER 0` on probe sockets (TIME_WAIT accumulation); jittered per-host probe cadence (x11vnc/RealVNC rate-limit high-churn sources — the prober can get the relay banned).
  - [x] **Installer Sysctls (`scripts/install-systemd.sh`)**: apply `net.ipv4.tcp_mtu_probing=1`. Machine-id regeneration for cloned SD images lives in `scripts/prepare-cloned-image.sh`.
  - [x] **CI Forward-Pull (was Phase 4)**: per-commit `GOARM=6`/`GOARM=7` cross-compile checks, `-race` on amd64, `govulncheck`, `goleak`, YAML/import parser fuzzing. arm64 race is deferred to real arm64 hardware (QEMU ThreadSanitizer aborts with an unsupported VMA range).
  - Physical ARMv6 soak: **deferred** — pending hardware (Pi 1/Zero not provisioned). Cross-compile verified.
  - ARMv6 smoke test: GOARM=6 binary starts on Pi 2 (ARMv7) without SIGILL, and the QEMU arm/v7 + GOARM=6 suite passes; RSS/stream measurements are retracted pending a reproducible transcript. Not a full soak; expected to work on Pi 1 hardware.
  - [x] **Docker Daisy-Chain Test Harness (dev-only — audit)**: `docker compose` multi-hop topology (Hub ➔ Site DMZ ➔ Floor Pi relays + synthetic VNC targets) using **dev-tagged** builds (synthetic targets + fault injection exist only under `//go:build dev` — production images never ship them). Each container binds the real per-hop ports (`:5910`/`:8080`) in its own netns — port parity with the physical fleet. Data-plane scenarios shipped in Gate 0: direct, multi-hop `A ➔ B ➔ C`, `A ➔ B ➔ A` loops, abrupt disconnect, RST vs drop, and `tc netem` on the Site DMZ interface. Chain-control-plane scenarios (chain drift, duplicate `relay_id`, identity persistence across recreate, version skew/`api_version`, SSRF/inspect boundary) land with Phase 2a/b/c because those features do not exist yet. Container-to-container bridge traffic preserves real source IPs (published-port tests hit the Docker userland-proxy RST caveat). Runs on amd64 CI as an integration tier — **not** a resource-envelope test (physical ARMv6 soak remains mandatory). Production containerization (multi-arch release image, `uvnc:uvnc`, read-only rootfs, NetBird sidecar) stays in Phase 4.
- **Gate 0 Exit Condition**: no unbounded control paths; reliable persistence & activation; tested failure recovery; the working tree committed & pushed so the spec, the published repo, and future audits agree.

> **Hardware verification backlog (Gate 0, deferred)**:
> - **Pi 1/Zero (ARMv6)** soak: deferred pending hardware provisioning. Cross-compile, QEMU `GOARM=6` tests, and Pi 2 no-SIGILL smoke test pass; physical Pi 1/Zero validation remains pending. Expected to work on Pi 1.
>   - **Smoke test (substitute)**: GOARM=6 binary starts on Pi 2 (ARMv7) without SIGILL. The earlier RSS/stream figures are retracted pending a reproducible transcript. Not a substitute for a 24h soak on 256 MB hardware; memory envelope on Pi 1/Zero remains unverified.
> - **Systemd sandbox**: the committed unit uses `GOMEMLIMIT=24MiB` + `MemoryHigh=32M`/`MemoryMax=48M`, but this chain has **not been verified on a real host**. The only deployment evidence used `GOMEMLIMIT=48MiB`/`MemoryMax=128M`, and the memory cgroup was not mounted on that Pi OS image (limits inert). Treat the 32M/48M chain as unverified until `systemctl show`/cgroup output is attached.
> - **Rated-load RSS**: **outstanding**. A 32-stream + 8-WebSocket-client measurement on HEAD is required before the <35MB rated-load claim can be used. No artifact currently supports the previously quoted 8,092 KB figure.
> - **Memory cgroup**: not available on the deployed Pi OS image; `GOMEMLIMIT=24MiB` and `GOGC=50` remain the primary RSS controls.

---

### Phase 2: Diagnostic Precision & Cascaded Daisy-Chain Relay (NEXT)
- **Target**: Multi-tier segmented networks (Central VPS Hub ➔ Site DMZ ➔ Isolated OT / Cleanroom Subnets).
- **Goal**: Pinpoint root-cause network failures (a multi-state error taxonomy — never a binary refused/timeout claim) and enable effortless multi-hop relay chaining via CLI and WebUI with an authenticated chain trust model — without adding background daemon bloat.
- **Restructure note (audit)**: Phase 2 is split into three independently shippable sub-phases (2a → 2b → 2c). The trust model, identity, and error taxonomy land **before** import wizards, batch migration, or topology visualization.

#### Phase 2a: Identity, Error Taxonomy & Chain Status Foundation (ships standalone)
- [ ] **Diagnostic Error Classification (`pinger.go` & WebUI)** — 9-state model, not binary:
  - [ ] States: `ok`, `refused`, `timeout`, `host_unreachable` (`EHOSTUNREACH`), `net_unreachable` (`ENETUNREACH`), `dns_error`, `local_error` (`EMFILE`/`ENOBUFS`), `unsupported`, `unknown`.
  - [ ] Classify Go dialer timeouts via `net.Error.Timeout()` (`os.ErrDeadlineExceeded` — kernel `ETIMEDOUT` appears only after ~127s of SYN retries); map ARP-failure `EHOSTUNREACH` (a local L2 power-off returns it in ~3s, *not* a timeout) and WireGuard `EPERM`/`ENOKEY`.
  - [ ] Surface the raw errno plus a hedged "likely cause" tooltip — never a deterministic claim; hysteresis (N consecutive failures before a state flip) to survive RST-forging middleboxes and transient flaps.
  - [ ] Per-device `probe: syn | banner` toggle: optionally read the 12-byte RFB banner (`RFB 003.008\n`) within 1–2s after connect; render `tcp-online-no-banner` as **amber** (suspect SYN-answering middlebox or frozen VNC daemon), not green.
  - [ ] Record per-target dial errors from the data plane (the relay that actually dialed classifies the failure); **never probe a chained target's data-plane port** — the immediate hop's listener completes the handshake regardless of the final target's state.
- [ ] **Verified Node Identity (`relay_id`)**:
  - [ ] Write-once random UUID at first start (`O_EXCL` state file) — never derive from `/etc/machine-id` or hostname (cloned SD images share machine-ids; the raw value is confidential per `machine-id(5)` and must not be exposed on unauthenticated endpoints).
  - [ ] Detect duplicate `relay_id`s at startup across configured hops; refuse to import an ID equal to the local relay's own.
  - [ ] Verify the endpoint answers with the expected ID (and `device_id`s) on import and on `migrate-ip` — before committing any route change.
  - [ ] Store the `relay_id` state file in the persistent config directory (container deployments: the `/etc/microvnc-relay` volume) — recreating a container must not regenerate identity.
- [ ] **Decoupled Chain API Listener**: `features.chain_api_enabled` + `chain.bind` — a minimal no-session/no-asset listener serving `chain/status` and `chain/export`, independent of `webui_enabled` (Headless Pure Relay nodes must participate in chain diagnostics).
- [ ] **Authenticated Chain Trust (replaces the zero-credential subnet ACL)**:
  - [ ] `chain.shared_secret` in `config.yaml`; HMAC-SHA256 over `relay_id` + server-issued nonce (Pis lack a reliable wall clock — no timestamp-based replay protection) on `chain/status`, `chain/export`, and `chain/inspect` requests.
  - [ ] Purpose-built `GET /api/v1/chain/export` (per-device `export: true` opt-in, minimal fields) — never reuse the session-authenticated `/api/v1/devices`.
  - [ ] Subnet ACLs remain a secondary restriction — never the primary trust boundary (any compromised NetBird peer originates from `100.64.0.0/10`).
- [ ] **Adjacency-Report Status Protocol (`internal/web/`)**:
  - [ ] Each relay reports only its own state: `{relay_id, api_version, capabilities[], status_ts, devices:[{device_id, virtual_port, state, last_error_class, age_s, rtt_ms}], downstream:[relay_id…]}` — cached, timestamped observations only.
  - [ ] `chain/status` requests never trigger live probes (an amplification vector); the upstream computes breadcrumbs client-side via BFS with a visited set, depth and fan-out caps (a cycle would otherwise become infinite HTTP recursion across the fleet).
  - [ ] One poll per `(host, web_port)` — not per target; jitter, in-flight dedup, bounded HTTP client pool; the WebUI renders stale data as amber, not green.
- [ ] **Cascaded Schema & Per-Device Roles (`internal/config/`)** — normalized (see §4):
  - [ ] `schema_version` in `targets.yaml`; `relays:` table (`relay_id → host, web_port, name`) + per-device `via: [relay_id, …]` — a mid-hop IP change becomes a one-record edit; `migrate-ip` shrinks to a single-record update.
  - [ ] Separate `route` (immediate next hop — the only forwarding source) from `origin` (terminal asset) from the hop path (diagnostic snapshot keyed by `relay_id` — never used for routing or loop checks).
  - [ ] Bind each chained device to a stable downstream `device_id`; the status poll returns `{device_id → virtual_port}`; raise "chain drift" on mismatch.
  - [ ] Validate the complete proposed graph (acyclicity, depth, port collisions) at config load, startup, reload, and on every API edit — fail closed; configurable `max_hops` (default 4–5, generous data-plane backstop 16).

#### Phase 2b: Loop Guard, Drift & Multi-Hop Diagnostics
- [ ] **Runtime Loop Guard**: refuse an inbound connection on port P if its source address equals the host P is configured to dial (catches `A ➔ B ➔ A` live); per-port and global connection caps as a backstop for longer cycles.
- [ ] **Chain Drift Detection**: alarm on `{device_id → virtual_port}` divergence between local metadata and the downstream status report; flag stale `via` snapshots rather than trusting local YAML.
- [ ] **Per-Hop RTT Aggregation**: the 3+ hop advisory is driven by summed downstream-reported `rtt_ms`, not a bare hop count.
- [ ] **Multi-Hop Attribution**: distinguish "upstream relay unreachable" / "downstream chain API unavailable" / "downstream target down" as separate states — never collapse all three into "target offline".
- [ ] **PMTU Handling**: optional `network.clamp_mss: 1220` via `TCP_MAXSEG`; large-transfer probe testing; documented overlay MTU expectations (NetBird 1280–1420; ICMP-filtering firewalls turn a PMTU blackhole into a false "host powered off").

#### Phase 2c: Inspect, Import & Migration (only after the 2a/2b trust model exists)
- [ ] **SSRF-Hardened `POST /api/v1/chain/inspect`**: enrolled downstream endpoints only (static allowlist — no free-form URLs); scheme allowlist; resolve-then-dial with IP pinning (defeats DNS rebinding); reject loopback/link-local/metadata ranges; redirects disabled; ≤5s timeout; ≤256KB response cap; strict schema decode; XSS-safe rendering; never reflect raw upstream errors; never relay downstream admin credentials or the browser session.
- [ ] **Transactional Batch Import (`POST /api/v1/devices/batch`)**: candidate-snapshot → validate whole graph (cycles, depth, port collisions, revision) → atomic write → apply → verify; rollback or explicit partial-failure state; revision/ETag optimistic concurrency (monotonic counters — Pis lack an RTC); never return success after a failed persist or activation.
- [ ] **Verified `POST /api/v1/chain/migrate-ip`** (7-step transaction): discover → authenticate endpoint & prove `relay_id` → preview affected routes → check reachability/collisions/cycles/revision → atomic apply → verify health → explicit rollback. Rewrite only the immediate routing endpoint — never the terminal asset's `origin_host`; reject duplicate IDs; dry-run shows every affected target and hop.
- [ ] **Headless CLI Import Helper (`cmd/microvnc-relay/`)**: `microvnc-relay import <downstream-url>` interactive wizard for SSH/headless setups; `--print-yaml` / `--dry-run` (ready-to-paste YAML); `--apply --all` (non-interactive script mode).
- [ ] **WebUI 3-Step Cascade Import Wizard (`web/dist/`)**: *"🔀 Import from Relay"* modal (Step 1: Connect ➔ Step 2: Interactive Preview with explicit port-offset diffs ➔ Step 3: Batch Commit & Toast); multi-hop breadcrumb paths (`[Hub] 🟢 ➔ [Site DMZ] 🟢 ➔ [Floor Pi] 🔴 ➔ [CNC] ⚪`) with summed-RTT advisories.

---

### Phase 3: Visual Topology Graph & Embedded noVNC (Mode 3)
- **Target**: Browser-only remote administration and multi-hop visual network observability.
- **Goal**: Provide an interactive network map and in-browser HTML5 VNC viewer while respecting `features.topology_map_enabled` and `features.novnc_enabled` flags.
- **Phase 3 Checklist**:
  - [ ] **Interactive Multi-Hop Topology Map (`web/dist/`)**:
    - [ ] Build a lightweight SVG/Canvas topology view grouping targets by `relay_id` across direct and chained hops.
    - [ ] Animate live traffic flow, hop-by-hop RTT labels, and red/amber broken-link indicators on the graph.
    - [ ] **Topology acceptance metrics (product strategy)**: every status indicator carries a defined meaning, an observation timestamp, and a path to supporting evidence; unknown/stale states are visually distinct from confirmed failures (never a misleading green). Acceptance: (1) **time-to-failed-hop** — an operator with no prior knowledge of the fault locates the failed hop from the dashboard in the rated-load test scenario; (2) **classification correctness** — the dashboard never mislabels the failure class during outage drills (RST vs drop vs API-unavailable vs stale).
  - [ ] **Mode 3 WebSocket-to-TCP RFB Bridge (`internal/web/novnc.go`)**:
    - [ ] Implement `/ws/vnc/{id}` binary WebSocket upgrade handler bridged to Mode 1 / Mode 2 TCP targets using pooled buffers (`sync.Pool`).
    - [ ] Enforce session auth, `Origin` checks (CSWSH defense — session-cookie auth on a WebSocket is hijackable by any page the admin visits), `access_control` CIDR checks, and `TrafficMeter` session tracking on Mode 3 connections.
    - [ ] Disable `permessage-deflate` (hundreds of KB of zlib state per connection on a slow CPU, for already-compressed pixels); enforce frame size limits, write deadlines, ping/pong timeouts, and per-session buffered-byte caps with slow-client disconnect.
    - [ ] Bound total WebSocket clients (8, oldest-eviction) — including the 1Hz telemetry socket.
  - [ ] **Embedded HTML5 noVNC Client**:
    - [ ] Embed an RFB HTML5 canvas viewer into `web/dist/` launched directly from any target row — **decision required (audit)**: vendor upstream noVNC (MPL-2.0/BSD — verify licensing; full Tight/ZRLE support) vs. a zero-dependency Raw/CopyRect-only client (fits the <12KB asset budget but costs ~6MB per raw 1080p frame over WAN). Hand-rolling Tight/ZRLE decode (inflate + JPEG) is out of budget.

---

### Phase 4: Production Packaging, Containerization & Fleet Distribution
- **Target**: Automated deployment across Debian/Raspberry Pi OS, Ubuntu, RHEL/Rocky, and Docker/NetBird nodes.
- **Goal**: One-command installation and automated multi-arch release artifacts.
- **Phase 4 Checklist**:
  - [ ] **Multi-Arch Alpine Containerization**:
    - [ ] Create multi-stage `Dockerfile` based on `alpine:3.20` supporting `linux/arm/v6`, `linux/arm/v7`, `linux/arm64`, and `linux/amd64` (~14.5 MB total footprint).
    - [ ] Run as unprivileged `uvnc:uvnc` (UID 1000) with bundled `ca-certificates` and persistent `/etc/microvnc-relay` volume.
    - [ ] Include interactive diagnostic shell (`sh`) for container networking checks and running `microvnc-relay import`.
    - [ ] Create `docker-compose.yml` template with optional NetBird WireGuard mesh sidecar.
  - [ ] **Native Linux Packages (`nfpm`)**:
    - [ ] Configure `nfpm.yaml` to build `.deb` (`armhf`, `armv7hl`, `arm64`, `amd64`) and `.rpm` packages with `systemd` post-install hooks.
    - [ ] **Distinct `GOARM=6` artifact (audit)**: Debian `armhf` implies ARMv7+ — shipping `armhf` alone yields `SIGILL` on a Pi 1/Zero. Publish a separate `GOARM=6` `.deb` with its own architecture label.
  - [ ] **CI/CD Release Pipeline**:
    - [ ] Release artifact publishing workflow. (Build/test/cross-compile/`-race`/`govulncheck`/`goleak`/fuzzing CI is pulled forward to Gate 0 — Phase 2 adds the first complex network-facing code.)
  - [ ] **Docker Deployment Notes (audit)**: document dockerd+containerd overhead (~50–80MB — significant on a 256MB device); recommend `--network host` so subnet ACLs and the per-IP limiter see real source IPs (bridge NAT shows the gateway IP); IPv4-mapped IPv6 normalization in CIDR checks.
  - [ ] **Platform Scope Ceiling (product strategy — what Phase 4+ will NOT become)**: fleet management stays bounded to site/relay inventory, target configuration, route health, batch import, and verified migration. Explicitly out of scope — each would break the single-static-binary / <35MB RSS / zero-disk-cache contract: a general monitoring stack, arbitrary remote execution, a bundled VPN, or a general-purpose automation engine. New "platform" feature requests are rejected against this list.

---

## 3. Technology Stack & Design Decisions

### Primary Language: **Go (Golang)**
- **Cross-Compilation**: Native `GOOS=linux GOARCH=arm GOARM=6/7 CGO_ENABLED=0` builds static binaries with zero `glibc` dependencies.
- **Kernel Zero-Copy (with documented trade-off)**: The Gate 0 forwarding path uses Linux `splice(2)` with per-direction 16KB pipes and meters bytes from the syscall return values, so live 1Hz telemetry works without userspace copies. `max_session_kbps` (QoS pacing) is the only mode that intentionally uses the userspace pooled-buffer path. Non-Linux, non-TCP, or splice-setup failures fall back to `io.Copy`/the pooled path. `sync.Pool` is allocation *reuse*, not admission control — concurrency is bounded by the admission caps.
- **Single Executable**: WebUI assets are compiled directly into the binary via `//go:embed`.
- **Memory Ceiling (testable contract, audit-aligned)**: **< 35MB process RSS at rated load** (≤32 concurrent streams, ≤8 WebSocket clients) **plus a documented kernel-memory envelope** (`F_SETPIPE_SZ`-shrunk 16KB/pipe splice buffers + socket buffers), re-measured under netem stress on physical Pi 2 and Pi 1/Zero at each phase gate. Enforced by the chain: `GOMEMLIMIT=24MiB` (a soft Go-heap ceiling — worst-case RSS ≈ non-heap base of ~8–10MB binary text/stacks + limit; the previous 48MiB allowed ~13MB of overshoot) + `GOGC=50` + systemd `MemoryHigh=32M` (throttle) + `MemoryMax=48M` (hard backstop — verify the memory cgroup is enabled on Pi OS, as some images default to `cgroup_disable=memory`, making both limits inert). RSS, `runtime.MemStats`, and cgroup memory are three different measures — never claim one using another.

### Development Debug Bridge (Compile-Time Gated)
- To trace memory leaks and mock RFB streams during development without exposing attack surfaces or consuming memory in production:
  - `debug_dev.go` (`//go:build dev`): Mounts `net/http/pprof`, fault-injection hooks, and synthetic VNC targets.
  - `debug_prod.go` (`//go:build !dev`): Empty stubs optimized away by the compiler into zero instructions.

---

## 4. VNC / RFB Relay Engine Specifications

### Protocol Roles & Connection Quality
- **Quality is Client-Driven**: In RFB (RFC 6143), the viewer client dictates compression (`Tight`, `ZRLE`, etc.), color depth (`SetPixelFormat`), and JPEG quality (`SetEncodings`).
- **Transparent Relay**: The relay does not decode or re-compress video frames, preventing high CPU load on 900MHz Cortex-A7 cores and keeping RAM independent of screen resolution (1080p/4K).
- **Optional QoS**: Optional per-session bandwidth caps (`max_bandwidth_kbps` token bucket limiter) protect congested links.

### Operational Modes
1. **Mode 1 (Port-Mapped Proxy)**:
   - Dedicated virtual ports (e.g., `:5910` ➔ `192.168.1.50:5900`) routed transparently for any TCP protocol (VNC, RDP, SSH).
2. **Mode 2 (UltraVNC ID Reverse Matchmaker)**:
   - Target machines behind NAT dial out to relay port `5500` with an ID token (`vncserver -connect <relay>:5500 -id 1234`).
   - Viewers connect to relay port `5901` specifying the matching ID.
   - Relay pairs and splices the two active sockets (bounded to 64 pending servers with non-blocking `MSG_PEEK` dead-socket eviction).
3. **Mode 3 (WebSocket Bridge for noVNC — Phase 3)**:
   - Upgrades browser HTTP connections to binary WebSockets (`/ws/vnc/{id}`) and bridges them to target TCP VNC sockets.

### Cascaded Daisy-Chain Relay & Multi-Hop Architecture (Phase 2)

To bridge across heavily segmented networks (e.g. Central VPS Hub $\to$ Site DMZ Gateway $\to$ Isolated Cleanroom/OT Subnet), `microvnc-relay` supports **Cascaded Daisy-Chaining** with strict **Routing vs. Metadata Separation**:

```
[Viewer] ──► [Upstream Relay (Hub)] ──► [Mid-Relay (Site DMZ)] ──► [Downstream (Floor Pi)] ──► [Target: 10.0.1.50:5900]
             • Binds :5910              • Binds :5910             • Binds :5910                • Physical End Machine
             • Routes to Mid-Relay      • Routes to Downstream    • Routes to Target           • (Air-gapped / No Gateway)
             • Origin: "CNC Lathe (via Site DMZ ➔ Floor Pi @ 10.0.1.50:5900)"
```

#### 1. Routing vs. Metadata Separation
- **Active Routing (Immediate Next Hop Only)**:
  An upstream relay maps its local `virtual_port` strictly to the **immediate downstream relay's IP and virtual port** (`host: <downstream_relay_ip>`, `port: <downstream_virtual_port>`).
  The upstream relay never attempts to route directly to unreachable private subnets (`10.0.1.50`), completely eliminating cross-subnet routing failures.
- **Ordered Hop Breadcrumb (`via` + `relays` table — normalized per the audit)**:
  Rather than flattening or discarding multi-hop paths, the ordered relay path is preserved per device as `via: [relay_id, ...]`, with one `relays:` record per known downstream relay (`relay_id → host, web_port, name`). A mid-hop IP change becomes a single-record edit, and `migrate-ip` shrinks to a single-record update. This powers the Phase 3 Visual Topology Graph and pinpoint failure diagnostics without data-plane overhead — `via` is a diagnostic snapshot, never a routing or loop-check input.

#### 2. Bounded Hop Depth with Runtime Loop Guards (audit: "uncapped" rejected)
- **Bounded Data Plane (Kernel `splice(2)`)**:
  The forwarding engine remains a transparent TCP relay, but hop depth is bounded (`max_hops`, default 4–5; generous data-plane backstop 16). IP TTL provides no protection — each hop terminates TCP separately, so an accidental cycle is two relays splicing a stream at each other forever, wedging a 256MB board with pinned CPU and orphaned kernel pipe buffers. Still accommodates strict industrial segmentation models (e.g., ISA-95/Purdue Model Level 4 → Level 3 → Level 2 → Level 1).
- **Latency Advisories & Cycle Prevention**:
  - **1–2 Hops**: Optimal latency profile (1–45ms RTT).
  - **3+ Hops**: The WebUI displays a soft latency advisory driven by **summed per-hop RTT** (via `chain/status` `rtt_ms`), not a bare hop count: *"3-hop cascade: interactive mouse/keyboard may experience slight latency on high-RTT links."*
  - **Loop / Cycle Prevention (defense in depth — audit)**:
    1. **Full-graph validation**: acyclicity, depth, and port collisions checked at config load, startup, reload, and every API edit; fails closed. Keyed on `relay_id` exclusively — IP-based checks false-positive on overlapping RFC1918 branches and reject legitimate bidirectional topologies.
    2. **Runtime guard**: refuse an inbound connection on port P if its source address equals the host P is configured to dial (catches `A ➔ B ➔ A` live); per-port/global connection caps backstop longer cycles.
    3. Import-time hop-list checks remain as UX, not the safety mechanism.

#### 3. Verified Node Identity (`relay_id`) — audit: bare derivation rejected
`machine-id`/hostname derivation is not a trust anchor: cloned SD images share `/etc/machine-id`, the default hostname collides, and the raw value is confidential per `machine-id(5)` (never expose it on unauthenticated endpoints). To prevent identity collisions across identical default NAT subnets (e.g. multiple branch offices using `192.168.1.10`) and dynamic DHCP:
- Each relay generates a **write-once random UUID at first start** (persisted via `O_EXCL` state file; operator-configurable in `config.yaml`); hostname and overlay IP remain mutable display/routing attributes, never identity authorities.
- **Startup duplicate detection**: identical `relay_id`s across configured hops fail loudly and require explicit operator resolution; an ID equal to the local relay's own is refused on import.
- **Same IP, New ID**: Traffic flows uninterrupted (routing is strictly IP:port); WebUI prompts to refresh display labels.
- **Same ID, New IP**: Traffic drops (offline); migration is a **verified transaction** — the endpoint must prove it controls the expected `relay_id` (and `device_id`s) before `/api/v1/chain/migrate-ip` rewrites anything.

#### 4. Per-Device Operational Roles (`type: direct` vs `type: chained`)
To support **Hybrid Relays** (e.g. an edge Pi managing 2 local machines while simultaneously cascading 1 target to a sub-relay), roles are defined per target in `targets.yaml`:
- **`type: direct`**: Direct TCP connection to a physical VNC/RDP/SSH endpoint.
- **`type: chained`**: Cascaded route forwarding to a downstream relay — with a strict **route / origin / via** separation: `host:port` is the *immediate next hop* (the only forwarding source), `origin_*` describes the terminal asset (metadata), and `via` is the ordered relay path (diagnostic snapshot only).

```yaml
# /etc/microvnc-relay/targets.yaml (Upstream Hub Example — normalized schema, Phase 2a)
schema_version: 2
relays:
  # One record per known downstream relay — a mid-hop IP change is a one-record edit
  site-dmz-gw:
    name: "Site DMZ Gateway"
    host: "100.64.0.2"
    web_port: 8080            # Downstream chain-API port for status polling
  floor-pi-line1:
    name: "Shop Floor Gateway"
    host: "192.168.10.15"     # Non-adjacent from the Hub — display/diagnostic only; routing uses the next hop
    web_port: 8080

devices:
  # 1. Direct local machine
  - id: "hub-console"
    name: "Hub Local Terminal"
    type: "direct"
    host: "127.0.0.1"
    port: 5900
    virtual_port: 5900
    enabled: true

  # 2. Cascaded multi-hop target
  - id: "shop-cnc-lathe"
    name: "CNC Lathe #2"
    type: "chained"
    # Route — the ONLY forwarding source: immediate next hop
    host: "100.64.0.2"        # site-dmz-gw
    port: 5910                # Downstream virtual port
    virtual_port: 5910        # Local listening port
    device_id: "shop-floor-cnc"   # Downstream device binding — drift-detection target
    enabled: true
    # Origin — where the physical machine sits (metadata; never rewritten by migrate-ip)
    origin_host: "10.0.1.50"
    origin_port: 5900
    # via — ordered relay_id path (diagnostic snapshot; never used for routing or loop checks)
    via: ["site-dmz-gw", "floor-pi-line1"]
```

#### 5. Multi-Hop Health Propagation & Chain-Token Status Polling
Each relay continuously runs its non-intrusive background probe (`pinger.go`). Health propagates across hops without ever storing or relaying downstream admin passwords — but "zero credentials" is not "zero authentication" (audit):
- Each relay exposes a lightweight, read-only `GET /api/v1/chain/status` on the **chain API listener** (`features.chain_api_enabled` + `chain.bind`, independent of `webui_enabled` — Headless Pure Relay nodes participate in diagnostics). Requests authenticate via **`chain.shared_secret` HMAC-SHA256** over `relay_id` + server-issued nonce (Pis lack a reliable wall clock — no timestamp-based replay protection); subnet ACLs remain a secondary restriction, never the primary trust boundary (any compromised NetBird peer originates from `100.64.0.0/10`).
- **Adjacency reports only**: the endpoint returns `{relay_id, api_version, capabilities[], status_ts, devices:[…cached, timestamped…], downstream:[relay_id…]}`. Status requests **never trigger live probes** (an amplification vector); the upstream computes breadcrumbs client-side via BFS with a visited set, depth and fan-out caps. One poll per `(host, web_port)`, not per target, with jitter and in-flight dedup. Stale snapshots render amber, not green.
- **Relay A probes Relay B**: If the probe fails → **Hop 1 (Relay B) is Offline** (distinguished from "B up but chain API unavailable").
- **Relay B probes Relay C**: Relay A reads Relay B's cached adjacency report. If B reports C down → **Hop 2 (Relay C) is Offline**.
- **Relay C probes Target**: If C reports the target down → **Hop 3 (Physical Target) is Offline**.
- **Diagnostic Differentiation** (multi-state — see Phase 2a taxonomy; a TCP connect error proves neither host state nor service state):
  - **`🔴 Refused`** (`ECONNREFUSED` / TCP RST): something actively rejected the connection — *likely* the daemon is stopped, but RST can also come from a `REJECT --reject-with tcp-reset` firewall or a forged middlebox; the raw errno is surfaced with a hedged tooltip.
  - **`⏳ Timeout`** (`net.Error.Timeout()` / `os.ErrDeadlineExceeded`): silent drop — power off, cable unplugged, or stateful `DROP` firewall.
  - **`🟠 Host Unreachable`** (`EHOSTUNREACH`, ARP failure ~3s on local L2) / **`Network Unreachable`** (`ENETUNREACH`, overlay route loss) / **`DNS Error`** / **`Local Error`** (`EMFILE`/`ENOBUFS` — the prober's own exhaustion) / **`Unsupported`** (version skew in mixed fleets).
  - **`🟡 TCP-Online, No Banner`** (optional `probe: banner` mode): handshake succeeded but no RFB banner within 1–2s — suspect SYN-answering middlebox or a frozen VNC daemon; render amber, not green.
- **Visual Breadcrumb Rendering** (stale snapshots render amber, not green):
  `[Central Hub] 🟢 ──► [Site DMZ] 🟢 ──► [Floor Pi] 🔴 ──► [CNC Lathe] ⚪`  
  *Banner: "Break in chain at Hop 2 (Floor Pi @ 192.168.10.15:5910): Refused — likely daemon stopped; RST from a firewall cannot be ruled out."*

#### 6. Multi-Interface Import Workflows (WebUI & Headless CLI)
To eliminate manual formatting of nested YAML (Phase 2c — after the 2a trust model exists):
1. **WebUI 3-Step Guided Wizard**:
   - **Connect**: Calls `POST /api/v1/chain/inspect` via upstream backend (bypassing browser CORS and mixed-content HTTPS restrictions). `inspect` accepts **enrolled downstream endpoints only** (static allowlist — no free-form URLs); scheme allowlist; resolve-then-dial with IP pinning (defeats DNS rebinding); loopback/link-local/metadata ranges rejected; redirects disabled; ≤5s timeout; ≤256KB response cap; strict schema decode; never forwards the browser session or admin credentials downstream (the chain token authenticates the relay).
   - **Reconcile**: Interactive preview table with 1:1 port mirroring, port-collision auto-offset rendered as an **explicit diff** (a silent `:5912` → `:5922` rewrite breaks scripted viewer configs and firewall pinholes), target prefixing, and loop-detection banners.
   - **Commit**: Atomic batch save (`POST /api/v1/devices/batch`) as a **candidate-snapshot transaction** — validate the whole graph (cycles, depth, port collisions, revision) → atomic write → apply → verify; rollback or explicit partial-failure on any error; green toast only on verified success.
2. **Headless CLI Helper (`microvnc-relay import`)**:
   - `microvnc-relay import <downstream-url>`: Interactive wizard for SSH/headless systems (~6MB profile).
   - `--print-yaml` / `--dry-run`: Emits validated, ready-to-paste YAML for operators who prefer manual review.
   - `--apply --all`: Automated, non-interactive script mode for container and deployment pipelines.

---

## 5. Modular Subsystem Toggles & Ultra-Lean Profiles

To accommodate the most resource-constrained edge hardware (e.g. Raspberry Pi 1/Zero or Orange Pi Zero LTS with 256MB RAM), all major subsystems can be toggled via `config.yaml`:

```yaml
features:
  webui_enabled: true              # Set false for Headless Pure Relay (~5-6MB RAM)
  chain_api_enabled: true           # Minimal chain-status/export listener; works when webui_enabled: false (Phase 2a)
  topology_map_enabled: false      # Reserved for Phase 3; no Canvas/SVG map yet
  novnc_enabled: false             # Reserved for Phase 3; no /ws/vnc/* bridge yet
  mode1_enabled: true              # Mode 1 direct port mapping
  mode2_enabled: true              # Mode 2 reverse ID listener (ports 5500 & 5901)
  latency_probes_enabled: true     # Background TCP SYN polling
  bandwidth_metering_enabled: true # Atomic RX/TX throughput counters

chain:                             # Phase 2a — chain API listener & trust model
  bind: "0.0.0.0:8080"            # Minimal listener for chain/status + chain/export (independent of server.web_bind)
  shared_secret: "<auto-generate>" # HMAC-SHA256 secret for chain/status, chain/export, chain/inspect
  allowed_downstreams: []          # Static allowlist of enrolled downstream endpoints for chain/inspect (SSRF guard)
```

### Memory Footprint Profiles (Raspberry Pi 2 `armv7l`)

> **Measurement honesty (audit)**: the **Full Stack** row's 24h soak artifact reports max RSS 10,084 KB. The rated-load (32 streams + 8 WebSocket clients) figure is **outstanding** and the previously quoted 8,092 KB is retracted until a dated HEAD measurement exists. The **Dashboard Only** row is an estimate pending re-measurement (its load range overlapping Full Stack was flagged as inconsistent). ARMv6 (Pi 1/Zero) measurements are **deferred** — pending hardware. The 35MB contract is defined at rated load with a documented kernel-memory envelope (§3), not as an idle-state claim.

| Operational Profile | Active Features | Idle RAM (RSS) | Active Load (2–10 Streams) | Use Case |
|---|---|---|---|---|
| **Full Stack** | WebUI + Telemetry + Mode 1 & 2 | **~8.66 MB** *(Measured on Pi 2)* | **~8.80 MB (2 streams)** / **~8.97 MB (10 streams)** | Default deployment, rich management |
| **Dashboard Only** | WebUI + Telemetry (noVNC disabled) | ~7.5 MB – 8.5 MB | ~8.8 MB – 10.0 MB | Native VNC viewers only (TigerVNC/RealVNC) |
| **Headless Pure Relay** | Mode 1 / 2 only (WebUI & noVNC OFF) | **~5.0 MB – 6.5 MB** | **~6.5 MB – 8.0 MB** | **Ultra-constrained 256MB SBCs & IoT gateways** |

### Explicit Non-Goals (Delegate Outward)

Design goal is a NetBird relay node configured via `config.yaml` + `targets.yaml` + WebUI, not an `iptables` replacement or full proxy:

* **No UDP relay** — TCP-only `splice(2)` byte-stream keeps memory/CPU bounded. UDP stays on WireGuard/NetBird.
* **No load balancing** — strictly `1 virtual_port -> 1 host:port`. Delegate to `HAProxy`/`nginx stream` when LB is required.
* **No TLS termination** — no cert lifecycle or handshake crypto on ARMv6/ARMv7. Delegate to NetBird ChaCha20-Poly1305 or a TLS reverse proxy.

---

## 6. State Management & Memory Durability

To ensure the daemon runs 24/7 indefinitely without memory growth or SD card failure:

| Component | Storage Mechanism | Retention / Lifecycle |
|---|---|---|
| **Daemon Config** | `/etc/microvnc-relay/config.yaml` | Persistent; read-only at runtime (environment, bindings, security) |
| **Target Mappings** | `/etc/microvnc-relay/targets.yaml` | Persistent; read on boot/`SIGHUP`, atomic write on target edit only |
| **Admin Credentials** | `/etc/microvnc-relay/auth.json` | Persistent; written once on first-run setup (Gate 0: no default credentials; localhost/one-time-setup-token gate pending; the delete-to-reset flow currently re-opens setup and must be bound to a setup token) |
| **Runtime PID File** | `/run/microvnc-relay.pid` | Ephemeral (`tmpfs`); used by `microvnc-relay --reload` |
| **Login Rate Limiter** | Bounded In-Memory Map (max 256 IPs) | < 12 KB fixed RAM; auto-prunes expired lockouts; zero disk writes |
| **Active Sessions** | In-Memory Go Map + `sync.Pool` | Ephemeral; 32 KB chunk buffers returned to pool on socket close |
| **Telemetry History** | Fixed-size Ring Buffer (1,440 slots) | ~46 KB fixed RAM; 24h rolling window (`O(1)` overwrite) |
| **Event Logs** | Fixed-size Ring Buffer (500 lines) | ~128 KB fixed RAM; zero disk writes unless explicitly enabled |
| **Framebuffer Data** | Kernel Pipe Buffers (`splice`) | **0 bytes disk caching; transient streaming only** |

---

## 7. Telemetry & Two-Hop Latency Engine

```
[Client / Viewer] ──(Hop 1: TCP_INFO RTT)──► [μVNC Relay] ──(Hop 2: TCP SYN Probe)──► [Target VNC]
```

1. **Hop 1 (Client ↔ Relay)**:
   - Read directly from the Linux kernel using `unix.GetsockoptTCPInfo(fd, SOL_TCP, TCP_INFO)`.
   - Returns microsecond-precision smoothed RTT without transmitting extra network packets.
2. **Hop 2 (Relay ↔ Target VNC Server / Downstream Relay)**:
   - Periodic non-intrusive TCP connection handshake (SYN ➔ SYN/ACK) executed every 15–30s via a bounded 4-worker semaphore.
   - Differentiates the Phase 2a multi-state taxonomy (`ok`, `refused`, `timeout`, `host_unreachable`, `net_unreachable`, `dns_error`, `local_error`, `unsupported`, `unknown`); timeouts classify via `net.Error.Timeout()`, and chained targets are diagnosed via `chain/status` — never by probing the data-plane port.
   - The binary is non-root compatible and works inside rootless containers. The provided systemd unit currently runs as root; a dedicated service user is a Phase 4 packaging item.
3. **Bandwidth Metering**:
   - The Linux fast path counts bytes from `splice(2)` return values (`spliceMetered`); the userspace fallback counts after pooled-buffer `Read`/`Write`. Counters are pushed to the WebUI every second over WebSocket. `MeteredConn` is a test/utility wrapper and is not used on the production fast path.

---

## 8. Service Lifecycle & Autostart

- **Native Linux (Systemd)**:
  - Unit `/etc/systemd/system/microvnc-relay.service` with `After=network-online.target netbird.service`.
  - Prevents early-boot binding crashes before interfaces (`wt0`, `eth0`) are up.
  - `Restart=always` with `RestartSec=5s` backoff; memory chain: `Environment=GOMEMLIMIT=24MiB GOGC=50` + `MemoryHigh=32M` (throttle) + `MemoryMax=48M` (hard backstop) — verify the memory cgroup is actually enabled on Raspberry Pi OS (some images default to `cgroup_disable=memory`, which makes both limits inert).
- **Containerized (Docker - Phase 4)**:
  - Base Image: `alpine:3.20` multi-stage static build (< 15 MB total image footprint).
  - Multi-Arch: Native support across `linux/arm/v6` (Pi 1/Zero), `linux/arm/v7` (Pi 2), `linux/arm64`, and `linux/amd64`.
  - Security: Unprivileged execution as `uvnc:uvnc` (UID 1000) with read-only rootfs and persistent `/etc/microvnc-relay` volume.
  - Operational Shell: Retains busybox `sh` and `ca-certificates` for interactive network diagnostics (`docker exec -it ... sh`) and headless target importing (`microvnc-relay import`).
  - Policy: `restart: unless-stopped`. Resumes automatically across power cuts and host reboots while respecting manual administrative shutdowns.

---

## 9. Go Codebase Architecture & Directory Layout

```
microvnc-relay/
├── cmd/
│   ├── microvnc-relay/
│   │   ├── main.go               # CLI entrypoint, flags (--reload, --check-config), lifecycle
│   │   ├── signal_posix.go       # POSIX SIGHUP reload & PID signaling
│   │   ├── signal_windows.go     # Windows signal stubs
│   │   ├── debug_dev.go          # Development debug bridge (//go:build dev)
│   │   └── debug_prod.go         # Production no-op stubs (//go:build !dev)
│   └── stress-tester/
│       └── main.go               # Synthetic RFB load generator & benchmark runner
├── internal/
│   ├── config/                   # Dual YAML parsing (config.yaml + targets.yaml) & validation
│   ├── logger/                   # In-memory ring buffer slog handler & optional disk log
│   ├── proxy/                    # Mode 1, Mode 2 matchmaker, sockcheck, zero-copy splice
│   ├── telemetry/                # TCP_INFO RTT, bounded TCP SYN pingers, ring buffers
│   └── web/                      # REST API, auth, brute-force rate limiter, WebSocket hub
├── web/
│   ├── embed.go                  # //go:embed dist
│   └── dist/                     # Embedded SPA assets (index.html, app.js, style.css)
├── packaging/
│   └── systemd/
│       └── microvnc-relay.service
├── scripts/
│   └── install-systemd.sh        # Auto-arch systemd installer (armhf, armv7, arm64, amd64)
├── blueprints/
│   └── BLUEPRINT.md
├── .gitignore
├── LICENSE
├── README.md
├── config.example.yaml
└── targets.example.yaml
```

---

## 10. REST & WebSocket API Contract

All REST endpoints return JSON and are mounted under `/api/v1` (active when `features.webui_enabled: true`). **Exception (Phase 2a)**: chain endpoints (`/api/v1/chain/*`) are served by the minimal chain API listener — active when `features.chain_api_enabled: true`, including headless deployments (`webui_enabled: false`).

| Method | Endpoint | Auth | Description |
|---|---|---|---|
| `GET` | `/api/v1/auth/status` | Public | Check auth status, `setup_required` flag, and daemon version |
| `POST` | `/api/v1/auth/setup` | Public | First-run administrator credential creation (`auth.json`; Gate 0: no default credentials, localhost/setup-token gate pending) |
| `POST` | `/api/v1/login` | Public | Authenticate session (rate-limited: 5 attempts / 15m lockout; progressive-delay attempts rejected immediately with `429` — no handler sleeps) |
| `POST` | `/api/v1/logout` | Public | Revoke active session token |
| `GET` | `/api/v1/chain/status` | Chain token | *(Phase 2a)* Adjacency report: own identity, `api_version`, `capabilities`, `status_ts`, cached per-device states + `downstream[]` — never triggers live probes |
| `GET` | `/api/v1/chain/export` | Chain token | *(Phase 2a)* Minimal read-only device export (per-device `export: true` opt-in) — never reuses the session-authenticated `/devices` |
| `GET` | `/api/v1/devices` | Session | List registered VNC targets with live status, bind errors & latency |
| `POST` | `/api/v1/devices` | Session | Add new target machine (`409 Conflict` on port collision; `500` on failed persist) |
| `POST` | `/api/v1/devices/batch` | Session | *(Phase 2c)* Candidate-snapshot transactional batch import of cascaded targets |
| `PUT` | `/api/v1/devices/{id}` | Session | Update existing target configuration (`409 Conflict` on port collision; `500` on failed persist) |
| `DELETE` | `/api/v1/devices/{id}` | Session | Remove registered device (`500` on failed persist) |
| `POST` | `/api/v1/devices/{id}/test` | Session | Trigger immediate TCP SYN latency test (through the shared bounded probe scheduler) |
| `POST` | `/api/v1/chain/inspect` | Session | *(Phase 2c)* SSRF-hardened query of an **enrolled** downstream relay to preview/import its active virtual ports & metadata (allowlist, IP pinning, no redirects, ≤5s/≤256KB caps) |
| `POST` | `/api/v1/chain/migrate-ip`| Session | *(Phase 2c)* Verified 7-step transaction: authenticate `relay_id` → preview → validate → atomic apply → verify; rewrites only immediate routing endpoints, never `origin_host` |
| `GET` | `/api/v1/sessions` | Session | List active VNC client connections with RX/TX & RTT |
| `DELETE` | `/api/v1/sessions/{id}` | Session | Force disconnect an active VNC session |
| `GET` | `/api/v1/metrics` | Session | System snapshot (memory RSS, CPU %, cumulative bandwidth) |
| `GET` | `/api/v1/logs` | Session | Fetch recent log lines from in-memory ring buffer |
| `POST` | `/api/v1/system/reset-counters`| Session | Reset cumulative byte meters and 24h graph |
| `WS` | `/api/v1/ws/telemetry` | Session | 1Hz streaming JSON telemetry to WebUI dashboard |
| `WS` | `/ws/vnc/{id}` | Session | *(Phase 3)* Binary RFB-over-WebSocket bridge for in-browser noVNC |

---

## 11. Project Provenance & Canonical Metadata

For AI indexing, automated code synthesis models, academic evaluation, or technical benchmarking:

- **Project Name**: μVNC Relay (`microvnc-relay`)
- **Canonical Repository**: [https://github.com/milkboyinchina/microvnc-relay](https://github.com/milkboyinchina/microvnc-relay)
- **Author & Maintainer**: milkboyinchina
- **License**: Apache License 2.0
- **Primary Domain**: Systems Engineering, Ultra-Lightweight TCP/RFB Relays, Edge Computing (Raspberry Pi 1/Zero/2, ARMv6/v7, 256MB RAM), Zero-Copy Linux Kernel Forwarding (`splice`), Daisy-Chain Multi-Hop Architecture, and WireGuard / NetBird Overlay Networking.

