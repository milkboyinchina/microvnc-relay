report by minstral.ai think mode

# μVNC Relay — Architectural Audit, Second Pass (BLUEPRINT.md)

This pass re-examines the spec from the kernel up rather than the API down. Several findings below are new or sharper than the obvious layer: two internal inconsistencies in the memory model, a false-online diagnostic class the spec cannot currently see, and an identity-collision failure mode rooted in how Pi images are actually cloned in the field.

---

## 1. Architectural Scorecard

| Dimension | Score |
|---|---|
| Architecture, Protocol & Multi-Hop Design | 8/10 |
| Edge Resource Efficiency (<35MB & Zero-Cache) | 8/10 |
| Diagnostic & Failure Isolation Precision | 6/10 |
| Security, Auth & SSRF Protection | 5/10 |
| Phasing, Roadmap & Completeness | 6/10 |
| **OVERALL** | **66/100** |

Lower than a first-pass gut read would suggest, and the deduction is concentrated: the Phase 1 core is disciplined and the memory numbers are credible, but the spec's own guarantees contradict each other in two places (§2.1, §2.2), and the diagnostic taxonomy has a blind spot (SYN-answering middleboxes) that will make the flagship Phase 2 feature report confidently wrong answers in exactly the OT environments it targets.

---

## 2. Critical Flaws & Architectural Vulnerabilities

### 2.1 GOMEMLIMIT contradicts the RSS budget
The spec states `GOMEMLIMIT=48MiB` while committing to **< 35MB process RSS**. `GOMEMLIMIT` is a *soft* limit on Go runtime-managed memory, and it says nothing about binary text (a static Go binary with embedded WebUI is realistically 8–12MB of resident pages), goroutine stacks, or cgo-free but mmap'd runtime structures. A relay that legitimately grows to the 48MiB soft limit is already ~13MB over its advertised ceiling, and `MemoryMax=128M` won't fire until it's nearly 4× the budget. If <35MB is a real contract, the runtime limit must sit *below* the contract: `GOMEMLIMIT=24–28MiB`, with the remainder reserved for non-heap RSS. As written, the three numbers (35 / 48 / 128) form no coherent enforcement chain.

### 2.2 The zero-copy claim quietly disables metering — and the spec should say so
Go's `splice` fast path in `io.Copy` only engages when both ends are unwrapped `*net.TCPConn`. The moment `MeteredConn` wraps the connection (i.e., the moment bandwidth metering or QoS is enabled), every byte transits userspace through the 32KB pooled buffers. That's a correct design, but it creates an undocumented operational trade-off: **enabling the optional QoS token bucket on a Pi 1 silently converts zero-copy splicing into double-buffered userspace copying** on a 700MHz ARMv6 with a single core that must also run the 1Hz telemetry loop, the probe engine, and the GC. The spec presents metering and zero-copy as composable features; in reality they're mutually exclusive per stream. Also note: with true splice, byte counts must come from splice return values (kernel-side), not wrapped readers — verify `MeteredConn` has a metered-splice path or the WebUI will show 0 bytes/sec for unmetered streams.

### 2.3 The diagnostic taxonomy has a false-online blind spot
The `online`/`refused`/`timeout` enum assumes the RST/timeout signals originate from the host under test. Two real-world classes break this:

- **SYN-answering middleboxes.** Industrial security appliances, tarpits, and some PLC-adjacent firewalls complete the TCP handshake for *any* port to defer/detect scanners. The probe returns "online 🟢" for a dead VNC service, indefinitely. In the cleanroom/OT networks this project explicitly targets, this is not exotic.
- **Docker userland proxies / NAT gateways** can return RST on behalf of a dead backend → "Service Down" for a host whose container merely restarted.

Fix: make **application-layer liveness optional and configurable** — after connect, read the 12-byte RFB version banner (`RFB 003.008`) with a 1–2s timeout, then close. Three-state result: `tcp-online+banner`, `tcp-online-no-banner` (suspect middlebox — render amber, not green), `tcp-failed`. The trade-off is real (banner reads pollute target-side connection logs; some servers rate-limit or ban on repeated short connections — see §3.2), so make it a per-device `probe: syn|banner` toggle.

### 2.4 `relay_id` auto-generation has a field-proven collision mode
Deriving `relay_id` from `/etc/machine-id` or hostname is deterministic — but cloned Raspberry Pi images (dd'd SD cards, the single most common way these devices are provisioned in fleets) carry **identical machine-ids**. Two "different" relays then share an identity, and `POST /api/v1/chain/migrate-ip` batch-updates *both* fleets' targets to one IP, silently breaking the second site. Worse, loop detection keyed on `relay_id` treats them as the same node. Required: on startup, detect duplicate `relay_id`s across configured `chain.hops` and hard-fail/warn loudly; document `machine-id` regeneration in the installer script (`systemd-machine-id-setup` isn't run on most Pi clones).

### 2.5 Loop detection validates imports, not topology
As specified, validation only runs at import time and only checks whether *this* relay's ID appears in the *downstream's* current path. Cycles that close across multiple asynchronous import operations (Hub←DMZ-A, DMZ-A←DMZ-B, DMZ-B←Hub) are invisible to each individual check. Without an in-band hop-TTL, an accidental cycle becomes two relays splicing a stream at each other forever — on a 256MB board this manifests as permanently pinned CPU and kernel pipe buffers consumed on both nodes. You don't need to cap hop depth to add a TTL; even a generous depth guard (say 16) turns an infinite amplification loop into a logged, bounded failure. Keep import-time checks as line one, but the data plane needs its own guard.

### 2.6 Mode 2: the ID-signaling sub-protocol is unspecified, and the pairing race isn't addressed
- The spec says viewers connect to `:5901` "specifying the matching ID" but never defines *how* (first bytes of the stream? pre-handshake control frame? UltraVNC REP semantics?). This is exactly the kind of underspecified surface where implementations diverge and ID-squatting attacks live: nothing authenticates that a dialing "server" owns the ID it claims, so anyone on the overlay can claim ID 1234 and receive the next viewer's session.
- Concurrency: `MSG_PEEK` dead-socket pruning runs concurrently with accept/pairing logic. Pruning a socket from the pending pool while another goroutine has just selected it for pairing is the classic matchmaker race. The spec should mandate single-owner discipline (channel handoff or per-entry state machine), not just "non-blocking pruning."

### 2.7 Cross-relay status has no freshness or capability contract
`GET /api/v1/chain/status` returns downstream health, but the spec omits: (a) a `status_ts` timestamp — if the downstream probe engine wedges, the hub serves stale "all green" forever; (b) an `api_version`/`capabilities` field — a mixed-version chain where the downstream doesn't differentiate refused/timeout silently degrades the hub's diagnostics to binary, with no error surfaced. Every multi-node protocol needs a versioning story before the first heterogeneous fleet exists; retrofitting this after Phase 2 ships is painful.

### 2.8 Zero-credential trust is subnet-shaped, not identity-shaped
`chain/status` (subnet ACL) and `chain/inspect` (session auth, but the *backend* then fetches an arbitrary operator-supplied URL) together give any overlay peer a free topology map and any session holder an SSRF pivot: `POST /api/v1/chain/inspect http://169.254.169.254/` or `http://127.0.0.1:5500/` must not be fetchable. Required: scheme allowlist, post-DNS-resolution check against allowed subnets (defeats DNS rebinding), hard timeout (≤5s), response-size cap (≤256KB), and a configured allowlist of inspectable downstream hosts. Separately, replace IP-based trust on `chain/status` with a pre-shared per-chain token (HMAC over `relay_id` + timestamp) — ChaCha20-Poly1305 via NetBird protects the wire, but NetBird peers are not all trusted principals, and the spec currently conflates "encrypted" with "authenticated."

---

## 3. Overlooked Edge Cases & Operational Risks

1. **Probe-triggered target-side bans.** A full TCP connect-and-close every 15–30s per target is *visible* to the target: x11vnc, RealVNC, and several industrial VNC servers rate-limit or blacklist source IPs with high connection churn. The probe engine can get the relay's IP banned from the very machines it relays. Mitigation: jittered probe intervals, a shared probe cadence per host (not per device), and document `probe: syn` vs `banner` costs.
2. **`chain.hops` metadata rot.** Breadcrumbs are a snapshot in upstream `targets.yaml`. When the downstream reconfigures (renames a relay, adds a hop), upstream metadata silently drifts from reality; the topology graph and failure banners then confidently render fiction. `chain/status` should return the downstream's *own* breadcrumb for reconciliation, and the WebUI should flag drift rather than trusting the local YAML.
3. **NetBird MTU (1280) vs path MTU blackholes.** With ICMP filtered, a PMTU blackhole looks identical to `timeout` → the UI diagnoses "host powered off" for a healthy machine. Document `net.ipv4.tcp_mtu_probing=1` in the installer; it's one sysctl that removes an entire class of misdiagnosed support tickets.
4. **Errno coverage gap.** Overlay route loss yields `EHOSTUNREACH`/`ENETUNREACH`, not `ETIMEDOUT`. The enum needs a fourth state (`unreachable`) plus the raw errno surfaced to the UI; the refusal-to-admit ambiguity ("refused = usually service stopped, sometimes an active firewall REJECT") should be in the tooltip itself — overconfident diagnoses erode operator trust faster than honest ones.
5. **Half-open post-reboot sockets**: a target that reboots can still complete a probe against a stale conntrack entry → false "online" for the probe cycle; banner-mode probing (§2.3) also fixes this class.
6. **Session/WebUI slow surfaces.** The login limiter bounds failures per IP, but nothing caps concurrent *valid* sessions or concurrent WS telemetry connections; on a 256MB node, a handful of forgotten dashboard tabs each holding a 1Hz WS plus their goroutines is measurable. Cap WS clients (e.g., 8) with oldest-eviction.
7. **Port auto-offset breaks external automation.** The import wizard's "suggest `:5922` if `:5912` taken" is operator-friendly but silently invalidates any scripted viewer config or firewall pinholes pointing at the original port. The offset should be a highlighted, explicit diff in the commit step, never a quiet rewrite.
8. **`targets.yaml` concurrent editing**: WebUI batch writes vs operator `vim` + `SIGHUP`. Atomic rename prevents corruption but not lost updates; add an mtime/etag optimistic-concurrency check to the API.
9. **Phase 3 noVNC feasibility tension.** A "minimal, zero-dependency RFB HTML5 viewer in vanilla JS" is only realizable by requesting **Raw/CopyRect encodings exclusively** — which means no zlib/Tight, i.e., ~6MB per 1080p raw frame on a congested link. The 12KB budget and usable-over-WAN are mutually exclusive; Tight/ZRLE drag in pako (~45KB). Decide now: Raw-only LAN tool, or abandon the KB-count marketing for the viewer. Also note framebuffer memory lives browser-side — correctly keeping relay RSS flat — but the Mode 3 bridge's pooled buffers must be strictly per-session-bounded or a browser backpressure stall (slow tab, paused mobile) grows unbounded upstream buffers. Add per-session buffered-bytes caps with slow-client disconnect.
10. **Windows Tier-2**: `signal_windows.go` stubs exist, but nothing addresses SCM service integration; "runs 24/7" is undefined on Windows. Either scope Windows to interactive testing or add a `svc` wrapper to the Phase 4 list.

---

## 4. Actionable Improvements & Recommendations

**P0 — fix before any Phase 2 merge:**
1. **Realign the memory enforcement chain**: `GOMEMLIMIT ≤ 28MiB` + document expected binary/pages RSS + `MemoryMax` as a backstop, so 35/48/128 stop being three unrelated numbers.
2. **Harden `chain/inspect` as an explicit SSRF boundary** (scheme allowlist, post-DNS subnet check, ≤5s timeout, ≤256KB cap, host allowlist) and gate `/api/v1/auth/setup` to hard-403 once `auth.json` exists.
3. **Add pre-shared chain-token HMAC auth to `chain/status`** — identity-based, not subnet-based trust; ~50 lines, no TLS needed on ARMv6.
4. **Add a data-plane hop-TTL/depth guard** (bounded, generous) alongside import-time loop checks; key all loop detection on `relay_id` exclusively.
5. **Extend the health enum** to `online | banner-ok | refused | timeout | unreachable`, keep raw errno, and add per-device `probe: syn|banner` mode — this is the difference between the diagnostics feature working in OT networks and confidently lying in them.

**P1 — before mixed-version fleets exist:**
6. **Version and timestamp the chain protocol**: `api_version`, `capabilities[]`, `status_ts` in every status response; UI renders stale data as amber, not green.
7. **Duplicate-`relay_id` detection at startup** + machine-id regeneration step in `install-systemd.sh` (cloned-image fleets are the norm, not the edge case).
8. **Specify the Mode 2 viewer-ID signaling protocol** and pairing-state machine in the spec (single-owner handoff, handshake-completion timeout on paired sockets, symmetric viewer-side pending cap).
9. **Document the splice-vs-metering trade-off** and add a metered path to the splice fast path (count via splice return values), or state that metered streams forgo kernel zero-copy.

**P2 — roadmap restructuring:**
10. **Split Phase 2.** It currently bundles five subsystems (diagnostics, identity, schema, three chain APIs, two import UXs). Recommended split: **2a** = error classification + `relay_id` + `chain/status` + health propagation (all prereqs of everything else); **2b** = schema/`type: chained` + inspect/batch/migrate + CLI and WebUI wizards. Each is independently shippable and testable.
11. **Pull CI forward**: a `GOARM=6`/`GOARM=7` cross-compile check belongs in Phase 2's first PR, not Phase 4 — ARMv6 build breakage is cheapest to catch per-commit. Add a `targets.yaml` schema version field now so adding `chain:` fields in 2a has a defined upgrade/downgrade path.
12. **Verify the 1,440-slot ring's sample cadence** in the spec (1/min buffer vs 1/sec push) — as written, "24h history" is only true if the buffer downsamples independently of the WS push rate; make it an explicit, testable contract.

**Bottom line:** the Phase 1 core is the strongest part of this document — real hardware measurements, bounded data structures, and honest non-goals. The audit finding that matters most is cultural: the spec repeatedly converts *probabilistic signals* (RST, timeout, subnet origin, machine-id) into *deterministic claims* (service down, host offline, trusted peer, unique node). Every P0 item above is about restoring the distinction between what the network said and what the operator is told. Fix those, and the cascade architecture is genuinely sound; ship Phase 2 as written, and its headline features will produce confident, wrong answers precisely in the segmented OT environments they were designed for.
