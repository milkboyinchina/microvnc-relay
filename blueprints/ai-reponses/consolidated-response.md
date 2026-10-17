# μVNC Relay — Consolidated AI Audit Response

**Subject**: Architectural audit of `blueprints/BLUEPRINT.md` (Phase 2 cascaded-relay design)
**Consolidated**: 17 October 2026
**Sources**: Five independent AI audits in this directory — `argon.md` (Gemini/Argon 4), `chatgpt.md` (ChatGPT think mode), `claude.md` (Claude Sonnet 5.5), `deepseek.md` (DeepSeek), `minstral.ai.md` (Mistral think mode).
**Method**: Findings were cross-referenced, deduplicated, and ranked by reviewer agreement. Consensus items (flagged by 4–5 reviewers) are treated as high-confidence defects. Items flagged by a single reviewer are preserved as unique findings. Original files remain the authoritative per-reviewer detail.

> **Erratum — publish-state caveat (added 17 Oct 2026)**: ChatGPT's audit was performed against the *published* repository (origin @ `119d9f8`), which pre-dates substantial **uncommitted** local work implementing the blueprint's claimed Phase 1 features. Verification against the working tree shows 6 of its 10 code-level findings are already implemented locally (pending commit/push); 4 remain genuinely open. Its 40/100 therefore scores the published snapshot, not the working tree — see §5.1 for the verified breakdown.

---

## 1. Score Summary

| Reviewer | Overall | Architecture | Efficiency (<35MB) | Diagnostics | Security | Phasing | Audit Scope |
|---|---:|---:|---:|---:|---:|---:|---|
| Argon (Gemini) | **82/100** | 8.5 | 9.0 | 7.5 | 7.0 | 9.0 | Spec only |
| ChatGPT | **40/100** | 5 | 6 | 3 | 2 | 4 | **Spec + published repository** (origin @ `119d9f8`; stale vs working tree) |
| Claude Sonnet 5.5 | **57/100** | 6 | 7.5 | 4.5 | 4.5 | 6 | Spec only |
| DeepSeek | **66/100** | 7 | 8 | 6 | 5 | 7 | Spec only |
| Mistral | **66/100** | 8 | 8 | 6 | 5 | 6 | Spec only |
| **Average** | **62/100** | **6.9** | **7.7** | **5.4** | **4.7** | **6.4** | — |

**Weakest dimensions across all reviewers**: Security/Auth (4.7/10) and Diagnostic Precision (5.4/10). **Strongest**: Edge Resource Efficiency (7.7/10) — the Phase 1 memory discipline is universally credited as credible.

### Reading the 42-point scoring spread

The spread (40 → 82) is largely explained by **audit scope and strictness**, not factual disagreement:

- **ChatGPT (40)** is the outlier because it alone cross-checked the blueprint against the published repository and found claimed-complete Phase 1 features absent from code (see §5.1). **Post-verification caveat**: the published repo (origin @ `119d9f8`) pre-dates substantial uncommitted local work — 6 of its 10 code-level findings are already implemented in the working tree, so the 40 scores the *stale published snapshot*, not the working tree; what survives are 4 genuinely-open code defects (→ P0 item 1). It also applies the strictest bar: a control-plane feature that is *designed* but not *enforced* scores as missing.
- **Argon (82)** reviewed the spec as a design document, judged the roadmap and memory model favorably, and framed most flaws as "fixable before implementation."
- **Claude (57), DeepSeek (66), Mistral (66)** all reviewed spec-only and land in the middle, with identical verdict language.
- Despite the spread, **no reviewer disputes any other reviewer's facts**. Every consensus finding below is either explicitly stated or implicitly accepted by all five.

---

## 2. Consensus Verdict (unanimous, 5/5)

> **Phase 1's core relay design (immediate-next-hop routing, bounded memory, ring buffers, zero disk cache) is sound and disciplined. Phase 2 (cascaded relays) is NOT production-safe as written.**

All five reviewers independently conclude:

1. The **immediate-next-hop data plane + ordered hop metadata** separation is the right instinct and should be kept.
2. The **Phase 2 control plane fails on trust boundaries**: identity, authentication, SSRF, and loop prevention are underspecified.
3. The **diagnostic taxonomy overclaims**: it converts probabilistic network signals (RST, timeout, subnet origin, `machine-id`) into deterministic operator-facing claims.
4. Phase 2 must be **split into staged sub-gates** (2a/2b/2c) with the trust model and error taxonomy landing *before* import wizards, migration, and topology visualization. Four of five reviewers explicitly propose a 2a/2b/2c-style split; ChatGPT proposes an equivalent Gate 0–3 sequence.

Mistral's summary is the sharpest framing of the systemic issue:

> "The spec repeatedly converts *probabilistic signals* (RST, timeout, subnet origin, machine-id) into *deterministic claims* (service down, host offline, trusted peer, unique node). Every P0 item is about restoring the distinction between what the network said and what the operator is told."

---

## 3. Consensus Critical Findings (flagged by 4–5 of 5 reviewers)

### Agreement Matrix

| # | Finding | Argon | ChatGPT | Claude | DeepSeek | Mistral | Agreement |
|---|---|:-:|:-:|:-:|:-:|:-:|:-:|
| F1 | `chain/inspect` SSRF + auth contradiction | ✓ | ✓ | ✓ | ✓ | ✓ | 5/5 |
| F2 | Diagnostic taxonomy overstates TCP errors | ✓ | ✓ | ✓ | ✓ | ✓ | 5/5 |
| F3 | Headless relays excluded from chain health | ✓ | ✓ | ✓ | ✓ | — | 4/5 |
| F4 | Loop detection validates imports, not topology | — | ✓ | ✓ | ✓ | ✓ | 4/5 |
| F5 | `relay_id` is not a verified identity | — | ✓ | ✓ | ✓ | ✓ | 4/5 |
| F6 | Metering silently disables `splice(2)` zero-copy | ✓ | ◐ | ✓ | — | ✓ | 4/5 |
| F7 | `GOMEMLIMIT=48MiB` contradicts the <35MB RSS contract | ✓ | ✓ | ✓ | ✓ | ✓ | 5/5 |
| F8 | First-run setup is an admin-takeover race | — | ✓ | ✓ | ✓ | ✓ | 4/5 |
| F9 | Mode 2 matchmaker: slowloris + ID squatting | ✓ | ✓ | ✓ | ✓ | ✓ | 5/5 |
| F10 | Multi-hop health: staleness, recursion, fan-in | ✓ | ✓ | ✓ | ✓ | ✓ | 5/5 |
| F11 | Zombie sessions: keepalives, dead-peer, shutdown | ✓ | ✓ | ✓ | ✓ | ✓ | 5/5 |
| F12 | PMTU blackholes over WireGuard/NetBird overlays | ✓ | ✓ | ✓ | ✓ | ✓ | 5/5 |
| F13 | Config / `migrate-ip` are not verified transactions | ✓ | ✓ | ✓ | ✓ | ✓ | 5/5 |
| F14 | Chain trust is subnet-shaped, not identity-shaped | ✓ | ✓ | ✓ | ✓ | ✓ | 5/5 |
| F15 | No admission control — `sync.Pool` is not a memory cap | ✓ | ✓ | ✓ | ✓ | ✓ | 5/5 |

✓ = explicitly flagged ◐ = partially covered

---

### F1. `POST /api/v1/chain/inspect` — SSRF pivot + authentication contradiction (5/5) — **P0**

**The flaw (consolidated)**: The upstream backend fetches an operator-supplied arbitrary URL from a box that straddles network segments. The endpoint it needs to call (`/api/v1/devices`) is documented as Session-authenticated, yet the spec promises "zero credentials" — so either the downstream exposes its device list unauthenticated, or admin credentials are relayed through the upstream over plaintext HTTP. The fetch itself is a textbook SSRF primitive: loopback services, cloud metadata (`169.254.169.254`), OT-subnet port scanning via error/timing oracles, DNS rebinding, and redirect following. Worse, the untrusted response is written into `targets.yaml` (unbounded JSON → OOM on 256MB; YAML injection / stored XSS via `name` and `hops`; forged `relay_id` values poisoning loop detection).

**Consolidated fix (all five converge on the same shape)**:
- Accept a **registered relay ID or pre-enrolled endpoint**, never a free-form URL; restrict to a static `chain.allowed_downstreams` allowlist.
- Scheme allowlist (`http`/`https` only), **resolve-then-dial with IP pinning** (defeats DNS rebinding), reject loopback/link-local/metadata ranges, **disable redirects**.
- Hard timeouts (≤3–5s), response-size cap (`io.LimitReader` 64–256KB), strict schema decoding, device-count caps.
- Authenticate via a **scoped machine credential / chain token** — never relay downstream admin passwords, never forward the browser session cookie.
- Re-validate every imported field server-side; render names via `textContent` (XSS); never reflect raw upstream errors.
- Make inspect idempotent-safe and read-only before batch commit ever ships.

### F2. The diagnostic taxonomy overstates what TCP errors prove (5/5) — **P0**

**The flaw (consolidated)**: The blueprint's binary mapping (`ECONNREFUSED` → "host online, daemon stopped"; `ETIMEDOUT` → "host off/unplugged") is unsafe in exactly the OT/segmented networks this project targets. Consolidated failure classes the enum misses:

1. **Firewall `REJECT --reject-with tcp-reset`** forges RST → false "service stopped" (firewalld/iptables defaults on RHEL/Rocky/OpenWrt).
2. **SYN-answering middleboxes** — industrial security appliances and tarpits complete the handshake for *any* port → permanent false `online 🟢` (Mistral flags this as the false-online blind spot; Argon adds Windows VNC servers whose kernel completes the 3-way handshake even with a deadlocked GUI thread).
3. **Local L2 host powered off** → ARP failure returns `EHOSTUNREACH` after ~3s, *not* `ETIMEDOUT` — the "unplugged → timeout" mapping is wrong for exactly the Floor Pi case.
4. **Overlay route loss** (`wt0` down, WireGuard AllowedIPs mismatch) → `ENETUNREACH`/`EPERM`/`ENOKEY`.
5. **Go dialer semantics** (Claude, sharpest): a Go dial deadline returns `os.ErrDeadlineExceeded` ("i/o timeout"), *not* kernel `ETIMEDOUT` (which appears only after ~127s of SYN retries). Classify with `net.Error.Timeout()`.
6. **DNS failures** misreported as target timeouts (no `.local` mDNS support with `CGO_ENABLED=0`'s pure-Go resolver; no caching → every dial hits DNS).
7. **Local FD exhaustion** (`EMFILE`/`ENOBUFS`) blames the target for the prober's own exhaustion.
8. **Version skew** in mixed Pi fleets — downstream lacks the new endpoint → needs an `unsupported` state.
9. **Docker userland proxies / NAT gateways** return RST on behalf of dead backends.

**Consolidated fix** — the five proposals are compatible; merged taxonomy:
- States: `ok` / `online_verified` (banner read OK) · `refused` · `timeout` · `host_unreachable` (`EHOSTUNREACH`) · `net_unreachable` (`ENETUNREACH`) · `dns_error` · `local_error` · `unsupported` · `unknown`.
- **Optional per-device `probe: syn|banner` mode**: after connect, read the 12-byte RFB banner (`RFB 003.008\n`) with a 1–2s deadline → three-state result (`tcp-online+banner` / `tcp-online-no-banner` → render **amber, not green** / `tcp-failed`). All reviewers converge on *optional/configurable* (banner probes pollute target connection logs; some VNC servers rate-limit or ban high-churn sources — see U-M1).
- Surface the **raw errno** to the UI alongside a hedged "likely cause" tooltip, never as established fact.
- Add **hysteresis** (N consecutive failures before state flip) to survive RST-forging middleboxes and transient flaps.
- Record per-target dial errors from the **data plane** (the relay that actually dialed classifies the failure), not just the prober.
- **For chained targets, never probe the data-plane port** — the immediate hop's listener completes the handshake regardless of the final target's state (Claude C1). Use the status API only.

### F3. Headless Pure Relay nodes cannot participate in a chain (4/5) — **P0**

**The flaw**: `/api/v1/*` (including `chain/status`) is mounted only when `webui_enabled: true`, yet the Headless profile is the *headline* Pi 1/Zero deployment — the tier you most want diagnosable is the one that can't serve status. Upstream pollers get `ECONNREFUSED` on the web port, or worse, fall back to TCP-probing `:5910`, which is open on the Floor Pi even when the physical CNC target is dead → **false-positive "Online" for dead industrial targets**. A loopback `web_bind` (the safe default) has the same effect. Claude additionally notes the reverse trap: `MemoryMax` aside, the ~5–6MB headless binary is the most constrained tier being asked to carry the most new control-plane code.

**Consolidated fix**: Add `features.chain_api_enabled` + separate `chain.bind` — a minimal listener (no sessions, no static assets, no WebSocket hub, cached data only, <200KB RAM) that runs independently of `webui_enabled`. Alternatively forbid headless mode on chained topologies explicitly — but all four reviewers who flagged it prefer the decoupled listener. ChatGPT adds: define headless chain-API behavior *deliberately in the spec*, not as an accident of feature flags.

### F4. Loop detection validates imports, not topology (4/5) — **P0**

**The flaw**: Import-time checking of `relay_id`/IP against `chain.hops` only proves something about one snapshot. Cycles that close across **multiple asynchronous operations** (Hub←DMZ-A, DMZ-A←DMZ-B, DMZ-B←Hub), hand-edited YAML, or concurrent `import --apply --all` runs are invisible to every individual check. Because each hop terminates TCP separately, IP TTL provides no protection — an accidental A→B→A cycle is two relays splicing a stream at each other forever: a self-amplifying socket/goroutine/pipe-buffer bomb that wedges a 256MB board. Claude's three-part indictment: (a) import-time only; (b) the "relay_id *or IP*" check contradicts §4.3's own rationale (overlapping RFC1918 across branches causes false positives on duplicate `192.168.x.x` — non-adjacent hop IPs are meaningless outside their subnet; key on `relay_id` only); (c) it is relay-level, not port-level — legitimate bidirectional topologies get rejected. Mistral's nuance: you don't need a small hop cap to fix this — even a generous depth guard (~16) turns infinite amplification into a logged, bounded failure.

**Consolidated fix**:
- Validate the **full proposed graph** (acyclicity, depth, port collisions) at config load, on startup, on reload, and on every API edit — fail closed.
- **Runtime loop guard**: refuse an inbound connection on port P if its source address equals the host P is configured to dial (catches A→B→A); per-port and global connection caps as backstop for longer cycles.
- Configurable `max_hops` (default 4–5 per DeepSeek; generous 16 per Mistral) as a bounded-failure guarantee, plus a data-plane hop-TTL.
- Key all loop detection on `relay_id` exclusively; treat import-time checks as UX, not safety.

### F5. `relay_id` is an identifier, not proof of identity (4/5) — **P0**

**The flaw**: Deriving `relay_id` from `/etc/machine-id` or hostname fails in the field: Pi fleets are mass-imaged (dd'd SD cards → **identical machine-ids**), the default hostname `raspberrypi` collides, Alpine containers have no machine-id, and a restored filesystem can resurrect a duplicate. Two "different" relays sharing an identity means `migrate-ip` batch-rewrites **both** sites' targets to one IP, and loop detection treats them as one node. Claude adds: `machine-id(5)` marks the raw value as confidential — exposing it on an unauthenticated endpoint violates that. ChatGPT's framing: an identity returned by an unauthenticated HTTP endpoint can be spoofed; "Same ID, New IP" migration proves only that the remote *claimed* the same ID.

**Consolidated fix**:
- **Write-once random UUID at first start**, stored in a tiny state file created with `O_EXCL` (DeepSeek, Claude) — or `HMAC(machine-id, "microvnc-relay")` as a fallback with clone detection.
- Treat hostname and overlay IP as mutable display/routing attributes, never identity authorities.
- **Detect duplicate IDs at startup** across configured hops; refuse to import an ID equal to your own; require explicit operator resolution on collisions.
- On import and on `migrate-ip`, **verify the endpoint answers with the expected ID** (and expected `device_id`s) before committing any route change.
- Add `machine-id` regeneration (`systemd-machine-id-setup`) to the installer script — cloned images are the norm in Pi fleets, not the edge case.

### F6. Metering silently disables `splice(2)` zero-copy (4/5, +1 partial) — **P1**

**The flaw**: Go's `io.Copy` reaches the kernel `splice(2)` fast path only when both ends are bare `*net.TCPConn`. Wrapping connections in `MeteredConn` — which is the **default** (`bandwidth_metering_enabled: true`) — hides the type assertions and forces every byte through userspace 32KB pooled buffers. "Zero-copy" is therefore true only for the opt-out config, on a single-core 700MHz ARMv6 that must also run telemetry, probes, and GC. Mistral adds the inversion: with true splice, the WebUI would show **0 bytes/sec** for unmetered streams unless byte counts come from splice return values — kernel-side — not wrapped readers.

**Consolidated fix** (Argon's proposal, endorsed by the others in substance):
- Call `syscall.Splice(rfd, nil, pipeWriter, nil, chunkSize, 0)` directly in a Linux relay loop (`proxy_linux.go`): full kernel zero-copy **and** `n` feeds the `atomic.Uint64` RX/TX counters and token-bucket pacing with zero userspace allocations. Isolate in `*_linux.go` with `*_other.go` fallbacks per repo rules.
- Alternatively implement `ReadFrom`/`WriterTo` on the wrapper that delegates to the underlying `TCPConn` and counts the returned `n`. Accept and **document the trade-off**: token-bucket QoS inherently forces the copy path; metering and zero-copy are mutually exclusive per stream, not composable.
- Shrink kernel pipe buffers on constrained boards: `unix.FcntlInt(pipeFD, unix.F_SETPIPE_SZ, 16384)` → 75% less kernel slab per stream (Argon, unique detail).

### F7. `GOMEMLIMIT=48MiB` contradicts the <35MB RSS contract (5/5) — **P0**

**The flaw**: The three numbers (35MB RSS / 48MiB GOMEMLIMIT / 128M MemoryMax) form no coherent enforcement chain. `GOMEMLIMIT` is a *soft* limit on Go-runtime-managed memory only: it excludes the binary's resident text pages (8–12MB for a static Go binary with embedded WebUI), goroutine stacks, and mmap'd runtime structures. A relay that legitimately reaches the 48MiB soft limit is already ~13MB over the advertised ceiling, and `MemoryMax=128M` won't fire until nearly 4× the budget. ChatGPT insists on the three-measure distinction: **RSS ≠ `runtime.MemStats` ≠ cgroup memory** — never claim one using another. Claude adds: the memory cgroup is often **disabled by default on Raspberry Pi OS** (`cgroup_disable=memory`), so `MemoryMax` may be inert; and RSS is the wrong yardstick anyway — splice pipe buffers (64KB/pipe, 128KB per bidirectional stream) and TCP autotuned socket buffers are kernel memory RSS excludes. Argon quantifies: 50 streams ≈ 6.4MB of invisible kernel slab.

**Consolidated fix**:
- `GOMEMLIMIT=24–28MiB` (all reviewers' range: Argon 24, Claude 24–28, Mistral 24–28), `GOGC=50` retained.
- cgroupv2: `MemoryHigh=32M`, `MemoryMax=48M` as a real backstop (replacing 128M), **and verify `memory.max` is actually enforced** on Pi OS.
- Measure the real contract: RSS + `Slab` + socket memory under netem loss/latency, not loopback 10-stream tests.
- Keep headroom for handshake bursts, GC, logging, reconnection — a budget that only holds in ideal steady state will surprise (ChatGPT).
- Re-measure after every phase; treat <35MB as a **release gate**, not a design claim (DeepSeek P2).

### F8. First-run setup is a race-condition admin takeover (4/5) — **P0**

**The flaw**: `POST /api/v1/auth/setup` is public — first request to the web port wins. Wiping the Docker volume or deleting `auth.json` (the standard password-reset procedure) silently reopens it. No mutex or `O_EXCL` guards concurrent setup calls; ChatGPT found an actual race in the visible handler (`SetupRequired()` check and credential commit are separate operations — two concurrent first-run requests both pass). ChatGPT further found the shipped config **preloads a bcrypt hash of the default `"admin"` password**, so the first-run state can be bypassed entirely — either a known default credential or a lockout. Claude's adjacent finding (C12, unique detail): single-core bcrypt is a CPU DoS — one compare at cost 10 is ~1s of CPU on a 700MHz ARMv6; the limiter only caps failures *per IP*, so concurrent logins from 256+ addresses (or one IPv6 /64) stall the data plane; and the 256-IP map evicts under flood, dropping lockout entries and bypassing the limiter entirely.

**Consolidated fix**:
- Empty first-run state — **no default credential hash** in shipped config.
- Bind setup to **localhost only**, or require a **one-time setup token printed to stdout/journal**; hard-403 once `auth.json` exists.
- Atomic `O_EXCL` creation + mutex (setup, and `auth.json` writes generally).
- Wire the login rate limiter into the actual HTTP route (it exists in design but is not attached — see U-C1). Check the lockout **before** bcrypt; a semaphore of 1 on bcrypt; key IPv6 by /64; evict least-offending unlocked entries; **reject immediately instead of sleeping** (handler `sleep` is a slowloris against yourself).
- Set `Secure` on session cookies when HTTPS is in use; session idle + absolute expiry.

### F9. Mode 2 matchmaker: slowloris + ID squatting + unspecified ID protocol (5/5) — **P0**

**The flaw (consolidated)**:
- **Slowloris**: `maxPendingServers=64` is a cap, not a defense. 64 connections that open and send 0 bytes (no ID-read deadline) exhaust all slots and 64 goroutines instantly, locking out legitimate reverse servers. `MSG_PEEK` pruning only removes *dead* sockets — slow-but-alive junk sockets pass the liveness check and squat all 64 slots indefinitely.
- **ID squatting**: the ID is a guessable bearer secret and nothing authenticates that a dialing "server" owns the ID it claims — anyone on the overlay can register ID 1234 first, receive the next viewer's session, and capture the (weak, DES) challenge-response. Duplicate-ID arrival behavior (evict old vs reject new) is undefined; eviction enables hijacking, rejection breaks reconnects after Target A reboots.
- **Unspecified sub-protocol**: *how* viewers on `:5901` signal the matching ID is never defined (first bytes? control frame? UltraVNC REP semantics?) — exactly where implementations diverge. Pending-**viewer** queue, its bound, and viewer-before-server ordering are unspecified.
- **Pairing race**: `MSG_PEEK` pruning concurrent with accept/pair is the classic matchmaker race — needs single-owner discipline (channel handoff or per-entry state machine), not "non-blocking pruning".
- **TOCTOU** between the alive check and splice start.

**Consolidated fix**: `conn.SetDeadline(now+5s)` immediately on `Accept()` for both `:5500` and `:5901`, cleared only after ID parse/match; pending-**viewer** cap + timeout (symmetric with servers); per-IP and per-ID rate limits; duplicate-ID policy (verify ownership — shared prefix secret `ID:1234:token`, or `MSG_PEEK` liveness + evict-older with a takeover warning, since NAT rebindings orphan sockets before keepalive fires); accept backoff; global session cap; bind `:5500` to the overlay interface only.

### F10. Multi-hop health propagation: staleness, recursion, fan-in (5/5) — **P0/P1**

**The flaw (consolidated)**:
- **Staleness cascade**: unsynchronized 15–30s tickers per hop mean a 3-hop chain takes up to **N×30s (90+ s)** to propagate a failure to the Hub; breadcrumbs mix snapshots from different times and the UI oscillates 🟢→🔴→🟢.
- **Recursion amplification**: "bubble up multi-hop breadcrumbs" implies B's response embeds C's — if B's handler aggregates recursively, a cycle becomes infinite HTTP recursion across the fleet; one status request fans out into many network operations exactly when the network is degraded.
- **Fan-in**: the pinger is per-target, so N targets behind one relay means N status polls of the same `(host, web_port)`; many upstreams polling simultaneously = thundering herd.
- **No freshness/capability contract** (Mistral): without `status_ts`, a wedged downstream probe engine serves stale "all green" forever; without `api_version`/`capabilities`, a mixed-version chain silently degrades the Hub's diagnostics to binary.
- **Attribution conflation** (ChatGPT): "Hub can't query Floor Pi" has three materially different causes (Hub can't reach DMZ / DMZ up but API blocked / DMZ fine but reports Floor Pi down) that must not all render as "target offline".

**Consolidated fix**:
- **Adjacency-report design** (Claude's proposal, echoed by all): each relay reports only *its own* state — `{relay_id, api_version, capabilities[], status_ts, devices:[{device_id, virtual_port, state, last_error_class, age_s, rtt_ms}], downstream:[relay_id…]}` — from **cached, timestamped observations only**. `chain/status` must never trigger live probes (unauthenticated amplification vector).
- Upstream computes breadcrumbs **client-side via BFS with a visited set**, depth and fan-out caps — detects live cycles, no server recursion.
- **One poll per `(host, web_port)`**, not per target; jitter, backoff, in-flight dedup, bounded HTTP client pool.
- Each probe record: `observer_relay_id`, `target id`, `probe_kind`, `status`, `error_class`, `elapsed_ms`, `observed_at`, `last_success_at`, `stale_after`. UI renders stale data as **amber, not green**. Support `If-None-Match` + response size cap.
- Distinguish "hub can't reach DMZ" from "API unavailable" from "downstream target down" as separate states.

### F11. Zombie sessions: keepalives, dead-peer detection, shutdown semantics (5/5) — **P0**

**The flaw (consolidated)**: When a mid-chain peer loses power (cable pull, WireGuard peer death), no FIN/RST ever arrives. RFB is server-push and idle when static, so `splice(2)` blocks upstream **forever** — leaking FDs, pipe buffers (64KB kernel memory per pipe pair), and goroutines at every tier until ulimit or OOM. Go's default 15s keepalive doesn't fire while unacked data is in flight (default `tcp_retries2` ≈ 15 min). Mode 1/2/3 never specify half-close handling or per-session deadlines; two `io.Copy` goroutines can block indefinitely. ChatGPT found the lifecycle defect: proxy managers wait for tracked goroutines on `Stop()` but keep **no authoritative active-session registry** — a surviving stream blocks shutdown indefinitely (blocks config changes and restarts); with idle timeout off by default this is realistic.

**Consolidated fix**:
- `SetKeepAlive(true)` + `SetKeepAlivePeriod(15s)` on **every** accepted and dialed `net.TCPConn`, both directions, every hop.
- **`TCP_USER_TIMEOUT=30000`** (30s) via `SyscallConn().Control()` so unacked in-flight splice data over a dead tunnel terminates within 30s instead of 15 minutes.
- `CloseWrite`/`CloseRead` half-close handling with a bounded wait on the other direction.
- **Active-session registry**: stop admitting during shutdown, close all active sockets, wait only a fixed grace period, report sessions still pending; read deadlines alone don't bound a blocked write.
- Per-session and global caps (→ F15); NAT/conntrack idle timeouts need the keepalive too.

### F12. PMTU blackholes over WireGuard/NetBird overlays (5/5) — **P1**

**The flaw (consolidated)**: NetBird default MTU is 1280–1420; cascading across nested overlays (WAN WireGuard → VLAN/PPPoE → Wi-Fi) makes 32KB RFB framebuffer chunks exceed the inner path MTU. When intermediate firewalls drop ICMP Fragmentation-Needed, sessions **stall right after authentication** on the first full-screen rect — while the small SYN probe reads `online 🟢`. A PMTU blackhole is indistinguishable from "host powered off" in the current taxonomy.

**Consolidated fix**: `net.ipv4.tcp_mtu_probing=1` in the installer (one sysctl removes an entire class of misdiagnosed tickets); optional `network.clamp_mss: 1220` via `TCP_MAXSEG`; test with large bidirectional streams under different MTUs and collect retransmit/zero-window indicators. Claude notes each hop terminates TCP so MSS is per-hop (helps), and recommends `SO_SNDBUF` caps + `TCP_NOTSENT_LOWAT` to bound per-hop send-buffer stacking; the "3+ hops" advisory should be driven by summed per-hop RTT (via status `rtt_ms`), not a hop count.

### F13. Config and `migrate-ip` are not verified transactions (5/5) — **P0**

**The flaw (consolidated)**: ChatGPT found the concrete defect: `AddOrUpdateDevice` mutates in-memory config *then* validates — a failed validation leaves the invalid mutation in place; some handlers log save failures and still return HTTP success; live-reload errors are ignored. Result: WebUI says success, RAM has invalid state, disk has the previous state, listeners run older config — for batch cascade imports this activates some routes while others don't persist. Across reviewers: `migrate-ip` rewrites hosts by a **self-asserted, unverified `relay_id`** with no proof the new IP answers with the same ID; updating `hops[N].host` on the Hub does nothing for routing (the *next hop* holds the active socket) while leaving metadata permanently stale; duplicate IDs from cloned fleets cause mass misrouting; per-device `hops[]` copies denormalize (a mid-hop IP change should be one record edit, not N); concurrent WebUI writes vs operator `vim` + `SIGHUP` lose updates (atomic rename prevents corruption, not lost updates).

**Consolidated fix**:
- **Candidate-snapshot transaction**: build candidate → validate whole graph (cycles, depth, port collisions) → write temp + fsync + rename + fsync dir → apply listeners → verify; roll back or report explicit partial-failure on any error. **Never return success after a failed persist or activation.**
- `migrate-ip` as a **verified transaction** (ChatGPT's 7 steps): discover candidate → authenticate endpoint & prove relay identity → preview affected routes → check reachability/conflicts/cycles/revision → atomic apply → verify health → explicit rollback state. Rewrite only the immediate routing endpoint — never the terminal asset's `origin_host`.
- **Revision/ETag optimistic concurrency** on API writes and reload (Pis have no RTC — use monotonic time / counters, not wall-clock).
- `schema_version` in `config.yaml` and `targets.yaml` (4/5 reviewers) with a defined upgrade/downgrade path.
- Normalize the model (Claude): `relays:` table (`relay_id → host, web_port`) + `via: [relay_id…]` per device; separates `route` (next hop) from `origin` (terminal asset) from `chain` (diagnostic snapshot only, never used to forward).
- Keep `targets.yaml` writes **strictly on explicit human CRUD** — no status persistence, no cron `--apply --all` fsync churn (flash wear).

### F14. Chain trust is subnet-shaped, not identity-shaped (5/5) — **P0**

**The flaw (consolidated)**: `allowed_subnets: 100.64.0.0/10` means **every NetBird peer** can read the full topology — internal IPs, `origin_host`s, relay IDs, virtual ports, which sites are unreachable. That is mesh-wide information disclosure, not "zero credential" (Claude). A CIDR allowlist is a coarse trust boundary: any compromised peer enumerates the fleet; Docker bridge NAT degrades subnet ACLs and per-IP limiters alike (gateway IP); "encrypted" (WireGuard) is conflated with "authenticated" — NetBird peers are not all trusted principals (Mistral). And the inspected HTTP router applies `allowed_subnets` only in proxy handlers, **not as universal middleware** — new chain endpoints will not inherit it (ChatGPT, verified in code).

**Consolidated fix**: **pre-shared per-chain token** in `config.yaml` (`server.chain_token` / `chain.shared_secret`) with **HMAC-SHA256 over `relay_id` + server-issued nonce** (Pis lack a reliable wall clock — no timestamp-based replay protection; Argon/Claude/Mistral/DeepSeek all converge here; ~50 lines, cheap on ARMv6, no TLS needed). Add opt-in per-device `export: true` and a purpose-built **`GET /chain/export`** returning minimal fields — instead of reusing the session-authenticated `/api/v1/devices`. Authorization by mesh peer policy and role, not address range.

### F15. No admission control — `sync.Pool` is not a memory cap (5/5) — **P0**

**The flaw (consolidated)**: `sync.Pool` is an allocation-*reuse* mechanism, not an admission-control mechanism. ChatGPT's arithmetic: two 32KiB directions ≈ 64KiB of userspace copy buffers per active stream → 500 streams ≈ 31MiB of buffers alone — the 35MB budget is consumable by concurrency even when the pool works exactly as intended. Kernel costs stack on top (F7). Nothing caps: total active relay sessions, connections per target, accepted-but-unauthenticated sockets, concurrent handshakes, active probes, WebSocket clients (a handful of forgotten dashboard tabs each holding a 1Hz WS is measurable on 256MB — Mistral), or concurrent *valid* sessions. Probe concurrency is not globally bounded (ChatGPT, verified: create/update/test paths launch independent probe goroutines bypassing the nominal scheduler). A 5s handshake deadline bounds one handshake, not how many run concurrently.

**Consolidated fix**: hard, deterministic caps + defined behavior when reached: total active sessions, per-target sessions, pre-auth sockets, Mode 2 pending servers **and viewers**, simultaneous handshakes, active probes (one shared bounded scheduler — user-triggered probes go through it, never around it), WS clients (e.g. 8, oldest-eviction) with per-session buffered-byte caps and slow-client disconnect. Overload must be an explicit, observable state.

---

## 4. Partial-Consensus Findings (2–3 of 5 reviewers)

### P-F1. Port-positional routing has no device-identity binding (3/5: Claude, Mistral, ChatGPT) — **P1**
A's `:5910` means "whatever B currently maps at 5910". If B's operator edits/re-offsets/reorders ports, A's viewers land on a **different machine with no error** — in OT, connecting to the wrong CNC is a safety incident. The import wizard's "suggest `:5922` if `:5912` taken" auto-offset silently invalidates scripted viewer configs and firewall pinholes (Mistral). Fix: bind each chained device to a stable downstream `device_id`; status poll returns `{device_id → virtual_port}`; raise "chain drift" on mismatch; render port offsets as an explicit diff at commit, never a quiet rewrite; flag drift from `chain/status` reconciliation rather than trusting local YAML (§P-F2).

### P-F2. `chain.hops` metadata rot (4/5 in parts: Claude, Mistral, ChatGPT, Argon) — **P1**
Breadcrumbs are a snapshot in upstream `targets.yaml`; when the downstream reconfigures, upstream metadata silently drifts from reality and the topology graph "confidently renders fiction". Fold into the adjacency-report design (F10) and normalization (F13).

### P-F3. ARMv6 has never been tested on real hardware (ChatGPT, Claude, Mistral) — **P1**
The stress measurement is Pi 2 only; `GOARM=6` compile success is not a performance envelope. Claude: a Pi 1/Pi Zero needs a **distinct `GOARM=6` artifact and `.deb` architecture label** — Debian `armhf` means ARMv7+, so shipping armhf alone produces **SIGILL** on a Pi Zero. ChatGPT: ARMv6 verification must be in CI, and Phase 2 must not be gated by compile success alone; test with representative RFB traffic and slow-network conditions on physical hardware.

### P-F4. Web-plane security: cookies, CSRF, CSWSH, Origin (3/5: Claude, DeepSeek, ChatGPT) — **P1**
Session cookie is `HttpOnly` + `SameSite=Lax` but not `Secure` (verified in code — ChatGPT); with plaintext HTTP management and TLS delegated to the overlay, cookies are exposed. `/ws/vnc/{id}` with session-cookie auth = cross-site WebSocket hijacking by any page the admin visits; the 1Hz telemetry WS needs the same protections. Fix: enforce `Origin` checks, `SameSite=Strict`, CSRF protection on state-changing REST endpoints, `Secure` when HTTPS, no wildcard CORS.

### P-F5. DNS handling is a blind spot (3/5: Claude, ChatGPT, DeepSeek) — **P1**
`CGO_ENABLED=0` → pure-Go resolver: no `.local` mDNS (`raspberrypi.local` fails), no caching (every dial hits DNS), boot-time failure → unclassified error. Fix: distinct `dns_error` class (→ F2), record the resolved endpoint each probe used, short-TTL in-memory cache, re-resolution on dial; NetBird DNS names would eliminate most IP-migration pain (Claude).

### P-F6. The "zero flash wear" contract needs precision (3/5: ChatGPT, Claude, Argon) — **P2**
The app-level policy is coherent but doesn't bound the appliance: journald may persist stdout/stderr depending on host config; default `dphys-swapfile` lives on the SD card (swap defeats zero-wear); config/credentials intentionally persist. Defensible contract (ChatGPT's wording): *no RFB payload caching and no periodic telemetry/logging writes by the application; persistent writes limited to explicit config/credential changes; host-level logs per the deployment's durability policy.* Also verify daemon write access to the whole config dir (rename requirement) doesn't undermine read-only claims; document swap disable in the installer.

### P-F7. CI must move earlier (3/5: Claude, ChatGPT, Mistral) — **P1**
CI is scheduled Phase 4 but Phase 2 adds the first complex network-facing code. Bring forward: `GOARM=6`/`GOARM=7` cross-compile per commit, `govulncheck`, `-race` on amd64/arm64 (race detector unsupported on 32-bit ARM — hence a separate 32-bit build/test for atomic alignment), `goleak`, YAML/import parser fuzzing, and a `tc netem` multi-hop harness (blackhole, RST, loss, MTU, flap).

---

## 5. Unique Findings by Reviewer (flagged by one reviewer — preserved verbatim in substance)

These are high-value items only one reviewer caught; none are contradicted by the others.

### 5.1 ChatGPT — spec-vs-repo mismatch: expected publish lag; 4 findings survive (verified 17 Oct 2026)

ChatGPT was the only reviewer to attempt source verification — and it audited the **published** repository (origin @ `119d9f8`), which pre-dates substantial **uncommitted local work** implementing the blueprint's claimed Phase 1 features. The headline mismatch is therefore **expected publish lag, not unimplemented work**. Verified against the local working tree:

| ChatGPT finding | Published repo (as audited) | Local working tree (verified) | Status |
|---|---|---|---|
| Separate `config.yaml` / `targets.yaml` missing | Devices in one `config.yaml` model | `internal/config/targets.go` + `targets.example.yaml` (new); `SaveTargetsFile` writes targets "without touching config.yaml" | ✅ Implemented — commit/push pending |
| Mode 2 pending cap 64 + `MSG_PEEK` pruning missing | No cap or eviction in code | `mode2.go`: `maxPendingServers = 64`, `isSocketAlive` pruning, capacity rejection | ✅ Implemented — commit/push pending |
| Serial probe loop, no 4-worker scheduler | `ProbeAll` serial | `pinger.go`: bounded concurrency, `sem := make(chan struct{}, 4)` | ✅ Implemented — commit/push pending |
| Login limiter not wired into route | Limiter absent from handler | `api.go`: `a.limiter.Check/RecordFailure/RecordSuccess` in the login handler | ✅ Implemented — commit/push pending |
| `--reload` flag not implemented | No flag | `main.go`: `--reload`/`-r` + `handleCLIReload` + SIGHUP partial hot-reload (`signal_posix.go`/`signal_windows.go`) | ✅ Implemented — commit/push pending |
| `AddOrUpdateDevice` mutates-then-validates, no rollback | Invalid mutation left in place | `config.go:448-466`: backup copy + restore on validation failure | ✅ Implemented — commit/push pending |
| Default admin bcrypt hash preloaded | `admin` / hash of `"admin"` in defaults | Unchanged — `config.go:122-123` | ❌ **Still open** → F8 |
| Session cookie lacks `Secure` | `HttpOnly` + `SameSite=Lax` only | Unchanged — `auth.go:305-306` | ❌ **Still open** → P-F4 |
| `allowed_subnets` not universal HTTP middleware | Subnet check only in proxy handlers | No CIDR code in `internal/web` at all — a `chain/status` endpoint would ship unprotected | ❌ **Still open** → F14 |
| Handlers return success after failed saves | Save failure logged, 2xx returned | Unchanged — `api.go:234/252, 316/333, 348/358`: `slog.Error("Failed to save targets…")` then `StatusCreated`/`OK` | ❌ **Still open** → F13 |

Also unique to ChatGPT and unaffected by verification: the `-race`/acceptance-test matrix (§7), the five-gate roadmap, and the code-verification method itself.

> **Consolidation note (revised 17 Oct 2026)**: the mismatch does **not** reframe the other four reviews — they audited the spec, which describes the working tree, so their findings remain current. The notable irony: the four surviving code defects map exactly onto findings the spec-only reviewers had already raised (F8, F13, F14, P-F4) — the repo audit was *less current* than the spec audits. ChatGPT's lasting value is twofold: it independently confirmed, in code, the four genuinely-open P0s, and it proved the published repo lags the spec — which makes "fix the four, then commit and push" a P0 action in its own right (P0 item 1).

### 5.2 Claude (unique items)
- **C1 probe storm**: every chained-target probe is a real `accept()` on the downstream relay, which then dials *its* next hop — a 15–30s interval per target becomes a connect/drop storm at every tier, hitting VNC servers with half-open sessions.
- **C11 atomic alignment**: `atomic.AddUint64` on a misaligned struct field **panics on `GOARCH=arm`** — use the `atomic.Uint64` type (auto-aligned) + a 32-bit ARM `go vet`/test in CI.
- **C12 bcrypt CPU DoS** (folded into F8).
- **Probe TIME_WAIT**: probes accumulate TIME_WAIT on the prober side → `SO_LINGER 0` on probe sockets.
- **No RTC on Pi**: NTP sync jumps the wall clock — telemetry ring indexes, lockout timers, and session timers corrupt; use monotonic time; HMAC requests need nonce-based (not timestamp) replay protection.
- **Boot ordering**: `After=network-online.target` without `Wants=` doesn't pull the target in; binding overlay IPs before `wt0` exists → `EADDRNOTAVAIL` crash loop. Use per-listener retry with backoff or `IP_FREEBIND`.
- **Probe starvation**: 4 workers × 3s timeout × 100 dead targets ≈ 75s/cycle vs a 15–30s interval — guard against overlapping cycles, prioritize.
- **In-band RFB diagnostics**: for ports tagged `protocol: rfb`, emit a spec-compliant RFB failure banner (version, zero security types, reason string naming the failed hop) — puts the diagnosis in front of the viewer's operator.
- **Mode 3 noVNC**: vendor upstream noVNC (check MPL-2.0/BSD licensing) instead of hand-rolling Tight/ZRLE (needs inflate + JPEG); disable `permessage-deflate` (hundreds of KB of zlib state per connection on a slow CPU, for already-compressed pixels).
- **Data-model normalization**: `relays:` table + `via:` (folded into F13); status as adjacency report (folded into F10).
- **`--network host` for Docker** so ACLs and the limiter see real source IPs; Debian armhf ≠ GOARM=6 (folded into P-F3).

### 5.3 Argon (unique items)
- **Metered splice via `syscall.Splice` return values** + `F_SETPIPE_SZ 16384` (folded into F6).
- **Kernel pipe memory invisible to GOMEMLIMIT**: two pipes (4 FDs) per bidirectional stream = 128KB unswappable kernel slab per stream; 50 streams ≈ 6.4MB (folded into F7).
- **`EHOSTUNREACH` after ARP expiry** (~3 failed broadcasts) — falls into an unhandled "Unknown Error" bucket if the pinger only switches on `ECONNREFUSED`/timeout (folded into F2).
- **RST sub-check heuristic**: if RTT < 0.5ms on a remote WAN target, warn a local firewall/middlebox is spoofing the RST.
- **Flash-wear trap on `targets.yaml`**: `fsync`-per-write + cron-driven `--apply --all` = repeated block erases; strictly human-CRUD-only writes (folded into F13).
- **`upstream_degraded` state**: immediate downstream online but reporting failure at hop N (folded into F2 taxonomy).
- **`clamp_mss` config** (folded into F12).

### 5.4 Mistral (unique items)
- **SYN-answering middleboxes / tarpits** as a first-class false-online source in OT networks (folded into F2) — "not exotic" in cleanroom/OT networks.
- **Probe-triggered target-side bans**: x11vnc/RealVNC and industrial VNC servers **rate-limit or blacklist** high-churn sources — the probe engine can get the relay banned from the machines it relays. Mitigate: jittered intervals, shared probe cadence per *host* (not per device), document `syn` vs `banner` costs.
- **`status_ts`/`api_version`/`capabilities`** freshness contract (folded into F10).
- **Ring-buffer cadence math**: the 1,440-slot (24h at 1/min) ring vs a 1Hz WS push rate — "24h history" is only true if the ring downsamples independently of the push rate; make it an explicit, testable contract.
- **noVNC feasibility tension**: a zero-dependency vanilla-JS RFB client is only realizable by requesting **Raw/CopyRect exclusively** — ~6MB per raw 1080p frame on WAN; Tight/ZRLE drags in pako (~45KB) vs the 12KB budget. Decide now: Raw-only LAN tool, or drop the KB-count claim. Framebuffer memory is browser-side (relay RSS stays flat) but the bridge's pooled buffers need strict per-session caps + slow-client disconnect.
- **Windows scope**: `signal_windows.go` stubs exist but nothing addresses SCM service integration — "runs 24/7" is undefined on Windows; scope to interactive or add a `svc` wrapper in Phase 4.
- **Port auto-offset breaks automation** (folded into P-F1).
- **"Encrypted ≠ authenticated"** framing for NetBird trust (folded into F14).

### 5.5 DeepSeek (unique items)
- **Per-subsystem memory budgets**: budget chain polling, inspect handlers, batch import, and Phase 3 WebSocket bridging separately, with stress tests for each (64 pending Mode 2 servers, slowloris clients, 10+ chained targets, slow WS consumers, batch-import rollback).
- **Per-hop RTT aggregation**: upstream should *sum* downstream-reported hop RTTs rather than only probing the immediate relay (Claude has the parallel thought in P-F12's RTT-driven advisory).
- **Optional per-target client ACLs for Mode 1/2** (P2): overlay membership alone is too weak for exposed LAN binds.
- **TLS termination documentation**: "no TLS termination" is acceptable only if the WebUI is overlay-only or localhost-only — state it as a mandatory condition, not a non-goal.
- **WebUI asset budget honesty**: the <12KB claim will not survive the topology map, import wizard, and noVNC launcher.

---

## 6. Consolidated Action Plan

### P0 — before any Phase 2 merge (release blockers)

1. **Close the four open code-level defects, then commit and push** (U-C1, verified 17 Oct 2026). The six headline spec-vs-repo mismatches are already implemented in the working tree — publication, not implementation, is the gate. Four findings remain open and map onto existing findings: remove the default `admin` bcrypt hash (→ F8, `config.go:122-123`); add `Secure` to session cookies (→ P-F4, `auth.go:305-306`); implement `allowed_subnets`/CIDR middleware so a `chain/status` endpoint can never ship unprotected (→ F14 — no CIDR code exists in `internal/web`); return 5xx on failed persists instead of log-then-2xx (→ F13, `api.go:234/252, 316/333, 348/358`). Fix these **before** pushing so the public repo never carries them, then commit and push so the spec, the published repo, and any future audit agree.
2. **Fix first-run auth** (F8): no default credential hash; localhost-or-setup-token; atomic `O_EXCL` + mutex; wire the limiter into the real login route; lockout check before bcrypt; bcrypt semaphore; IPv6 /64 keying; immediate reject (no sleep).
3. **Harden `chain/inspect`** (F1): enrolled endpoints only; scheme allowlist; resolve-then-dial IP pinning; deny loopback/link-local/metadata; no redirects; ≤5s timeout; ≤64–256KB body cap; strict schema decode; scoped machine credential; re-validate all imported fields.
4. **Establish chain trust** (F14): pre-shared token + HMAC (`relay_id` + nonce); purpose-built `GET /chain/export` with per-device `export: true`; role-based authorization, not subnet ACL; verify subnet middleware coverage on every new endpoint.
5. **Decouple chain API from `webui_enabled`** (F3): minimal `chain_api_enabled` listener, separate `chain.bind`, cached data only.
6. **Fix identity** (F5): write-once random UUID (`O_EXCL`); startup duplicate-ID detection; refuse self-import; ID verification on import and `migrate-ip`; machine-id regeneration in the installer.
7. **Loop prevention** (F4): full-graph validation at load/startup/reload/API-edit; runtime source-addr guard; `max_hops` default 4–5 (backstop 16); `relay_id`-only checks; port-level granularity.
8. **Realign the memory chain** (F7, F15): `GOMEMLIMIT=24–28MiB`; `MemoryHigh=32M`/`MemoryMax=48M` (verify cgroup memory is enabled on Pi OS); hard admission caps (sessions, pre-auth, Mode 2 pending servers *and* viewers, handshakes, probes, WS clients) with explicit overload states; shared bounded probe scheduler for all probe sources.
9. **Socket lifecycle** (F11): keepalives + `TCP_USER_TIMEOUT=30s` on every socket both directions; `CloseWrite` half-close; active-session registry with shutdown grace period; per-session buffered-byte caps.
10. **Mode 2 hardening** (F9): 5s ID deadlines on both listeners; pending-viewer cap; duplicate-ID policy; single-owner pairing handoff; per-IP/per-ID limits; `:5500` bound to overlay only.
11. **Config transactions** (F13): candidate-snapshot → validate → atomic commit → apply → verify; revision/ETag concurrency; never success-on-failed-persist; `targets.yaml` touched only on explicit human CRUD.
12. **Error taxonomy** (F2): 9-state model with raw errno + hedged hints; hysteresis; `net.Error.Timeout()` classification; per-device `probe: syn|banner`; data-plane dial-error recording; chained targets never probe data-plane ports.

### P1 — before Phase 3 / before mixed-version fleets exist

13. **Chain protocol versioning** (F10): `api_version`, `capabilities[]`, `status_ts` in every status response; stale → amber rendering; adjacency-report design with client-side BFS; one poll per `(host, web_port)`; jitter + in-flight dedup.
14. **Device-identity binding & drift detection** (P-F1, P-F2): `device_id` → `virtual_port` mapping in status; "chain drift" alarms; explicit port-offset diffs; `relays:` + `via:` normalization; `schema_version` in both YAML files.
15. **Metering/splice resolution** (F6): `syscall.Splice`-based metered path with return-value counting, or documented mutual exclusion; `F_SETPIPE_SZ=16KB`.
16. **Web-plane security** (P-F4): `Secure` cookies, `SameSite=Strict`, `Origin` enforcement on all WS endpoints, CSRF on state-changing REST, `permessage-deflate` off, frame/write limits, WS client cap.
17. **DNS class** (P-F5): `dns_error` state; record resolved endpoints; short-TTL cache; re-resolution on dial.
18. **ARMv6 verification** (P-F3): distinct `GOARM=6` artifact + package label; physical Pi 1/Zero soak; pull CI forward (P-F7): `govulncheck`, `-race`, `goleak`, parser fuzzing, `tc netem` multi-hop harness.
19. **Installer sysctls** (F12): `tcp_mtu_probing=1`; optional `clamp_mss`; document MTU expectations on overlay interfaces.
20. **Probe hygiene**: `SO_LINGER 0` on probe sockets; per-host cadence; anti-ban jitter (U-M1).

### P2 — packaging, roadmap, and honesty fixes

21. **Phase 2 split** (all reviewers): **2a** error taxonomy + relay identity + `chain/status` + health propagation + schema versioning (ships alone) → **2b** adjacency status, loop guard, drift detection → **2c** inspect/import/migrate once the trust model (items 3–4) is settled. Never ship the wizard UX before the auth model it depends on exists.
22. **Phase 3 decisions now**: vendor upstream noVNC vs Raw-only scope (U-M3, U-C1); `svc` wrapper or interactive-only scope for Windows (U-M4); ring-buffer cadence as an explicit contract test.
23. **Packaging corrections** (Claude, Mistral, DeepSeek): Debian `armhf` ≠ ARMv6 — distinct artifact; Docker overhead (~50–80MB for dockerd/containerd) documented for 256MB devices + `--network host` requirement; per-target client ACLs for Mode 1/2; TLS-termination precondition documented for any non-overlay WebUI exposure.
24. **Re-measure RSS at every phase** as a release gate (F7): netem stress, 64-pending Mode 2, slow WS consumers, batch import rollback, 72h soak with 200+ streams asserting goroutine count, kernel memory, and FD count.

---

## 7. Consolidated Roadmap (all five proposals merged)

| Gate | Content | Exit condition |
|---|---|---|
| **Gate 0 — Stabilize Phase 1** | U-C1: fix 4 open code defects + commit/push pending work; auth bootstrap/limiter (F8); Mode 2 admission bounds (F9); probe concurrency (F15); shutdown registry (F11); config transactions (F13); reload; ARMv6 CI | No unbounded control paths; reliable persistence & activation; tested failure recovery; published repo matches spec |
| **Phase 2a — Identity & schema** | Error taxonomy (F2); persistent verified `relay_id` (F5); `chain_api` listener (F3); route/origin/metadata separation + `schema_version`; graph cycle/depth validation (F4) | Tests for A→B→A, deeper cycles, stale metadata, duplicate IDs, colliding private subnets, concurrent edits |
| **Phase 2b — Diagnostics & status** | Adjacency-report API (F10); loop guard; drift detection (P-F1); token/HMAC trust (F14); staleness rendering | No misleading "host offline" from raw errno; no request-triggered probe storms; unauthorized polling rejected |
| **Phase 2c — Inspect, import, migrate** | SSRF-resistant read-only inspector → dry-run preview → atomic batch commit → authenticated verified migration (F1, F13) | Authenticated discovery; revision checks; accurate errors; tested rollback |
| **Phase 3 — Topology & noVNC** | Topology view from validated graph state; WS bridge only after session limits, auth, frame limits, backpressure, graceful teardown are tested | Soak + resource containment passed |
| **Phase 4 — Packaging & fleet** | Alpine multi-arch, native packages, installers — **but CI/build/test/release starts at Gate 0, not here** | ARMv6 artifact distinct; cgroup memory verified on Pi OS |

### Acceptance tests before any production declaration (merged from ChatGPT/Claude/DeepSeek)

- **Graph**: direct, 1-hop, 3+ hops, self-loop, multi-node loop, duplicated relay identity, stale chain metadata, concurrent import/migration.
- **Security**: bootstrap race, login throttling through the real HTTP endpoint, unauthorized health polling, forged relay identity, unsafe inspect URLs, redirects, DNS rebinding, oversized JSON, session-cookie/CSRF/CSWSH handling.
- **Failure isolation**: RST rejection, silent drop, downstream API disabled/headless, DNS failure, routing error, local FD exhaustion, stale status.
- **Resource containment**: max connections, max pending Mode 2 IDs, concurrent handshakes, slow readers/writers, abrupt disconnects, shutdown during live streams.
- **Soak**: 72h representative streams on Pi 2 **and** physical ARMv6, recording `/proc` peak RSS, cgroup memory, `Slab`, open FDs, goroutine count.
- **Data plane**: prove when `splice(2)` actually engages; measure metered, rate-limited, and WS paths independently (F6).
- **32-bit correctness**: atomic alignment (`GOARCH=arm` build + test); race detector on amd64/arm64.

---

## 8. Points of Reviewer Disagreement (minor, mostly emphasis)

1. **Overall severity** — Argon (82) treats most flaws as pre-implementation fixes; ChatGPT (40) treats design-without-enforcement as missing. Both are defensible lenses; the spread is scope, not contradiction — and verification (§5.1) confirms ChatGPT scored a stale published snapshot: 6 of its 10 code-level findings were already fixed in the working tree. The consensus verdict (§2) is unaffected.
2. **Hop-depth cap size** — DeepSeek: default 4–5. Mistral: a cap is optional; a generous guard (~16) suffices since the TTL matters more than the number. ChatGPT/Claude: configurable maximum, validated in the control plane. All agree *a* bounded guard must exist (F4 fix accommodates all three).
3. **Banner probing scope** — Argon: only for non-single-session servers (opt-in `probe_mode`). Claude: for all direct targets. Mistral/DeepSeek: per-device toggle. Consolidated: per-device `probe: syn|banner` (F2 fix) satisfies every position.
4. **`chain/status` in headless mode** — Argon offers a binary health socket as Option B; the other three prefer the minimal HTTP listener. Consolidated fix (F3) prefers the HTTP listener (compatible with the HMAC trust model) but does not forbid the binary probe as an alternative.
5. **Whether to forbid headless chained nodes outright** (DeepSeek's alternative) — no other reviewer endorses it; the decoupled listener wins on consensus.

---

## 9. One-Line Reviewer Verdicts (as issued)

| Reviewer | Verdict |
|---|---|
| **Argon** | 82/100 — strong design; fix splice-vs-metering, headless status, SSRF, keepalives, memory knobs before Phase 2. |
| **ChatGPT** | 40/100 — Phase 2 no-go as written; the spec and repository don't match; fix trust boundaries, diagnostics, and transactional config first. |
| **Claude** | 57/100 — Phase 1 memory discipline credible; Phase 2 diagnostics flawed at the foundation; fixes must land before Phase 2 code. |
| **DeepSeek** | 66/100 — directionally sound, unusually disciplined, but not production-safe for Phase 2; P0 gaps in health propagation, loops, inspect security, Mode 2, first-run auth. |
| **Mistral** | 66/100 — Phase 1 core is the strongest part; the spec converts probabilistic signals into deterministic claims; fix that distinction and the cascade architecture is genuinely sound. |

---

## 10. Consolidated Bottom Line

The Phase 1 core is genuinely strong — real hardware measurements, bounded structures, honest non-goals (all five agree). Phase 2 as written would ship its flagship features with:

- an inspection API that is an SSRF pivot (F1),
- a trust model based on subnet membership and unauthenticated identity (F5, F14),
- diagnostics that will confidently misreport failures in exactly the segmented OT networks it targets (F2, F12),
- a control plane with four verified-open code defects (U-C1: default admin hash, cookie `Secure` flag, absent subnet middleware, save-failure semantics) plus no admission control (F15),
- loop and zombie-session failure modes that wedge 256MB boards (F4, F11), and
- a memory contract whose three enforcement numbers contradict each other (F7).

Everything above is fixable, and the fixes are mostly small, local, and cheap (HMAC ~50 lines; `TCP_USER_TIMEOUT` one `setsockopt`; `GOMEMLIMIT` one env var). The single highest-leverage action is narrower than first consolidated: the six headline spec-vs-repo mismatches are **already implemented in the working tree** (verified 17 Oct 2026), so the gate is **fix the four surviving code defects and commit and push** — not a spec/code reconciliation. Then land 2a (identity, taxonomy, status API) before any import wizard, migration, or topology map exists.

**Recommended immediate sequence**: Gate 0 (fix the 4 open code defects, then commit & push) → P0 items 2–12 → Phase 2a → 2b → 2c, with the acceptance-test matrix (§7) as the gate criteria at each step.

---

*End of consolidated response. Per-reviewer detail, scoring rationale, and unique supporting arguments remain in the five source files in this directory.*
