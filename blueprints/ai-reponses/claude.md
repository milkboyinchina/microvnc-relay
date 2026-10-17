report by claude.ai claude sonnet 5.5

# μVNC Relay `BLUEPRINT.md` Architectural Audit

This audit is of the spec only, not the code. Items marked **[verify in code]** are things I'd expect to be true from the spec's wording but couldn't confirm.

## 1. Architectural Scorecard

| Area | Score |
|---|---|
| Architecture, Protocol & Multi-Hop Design | **6 / 10** |
| Edge Resource Efficiency (<35MB & Zero-Cache) | **7.5 / 10** |
| Diagnostic & Failure Isolation Precision | **4.5 / 10** |
| Security, Auth & SSRF Protection | **4.5 / 10** |
| Phasing, Roadmap & Completeness | **6 / 10** |
| **OVERALL** | **57 / 100** |

Phase 1's memory discipline is credible and the routing-vs-metadata idea is the right instinct. But the Phase 2 diagnostic design is flawed at its foundation, and the chain API has no coherent trust model. Several fixes need to land before Phase 2 code is written.

---

## 2. Critical Flaws & Architectural Vulnerabilities

**C1. The SYN probe cannot see past the first hop of a relay chain (fatal to the diagnostic goal).**
- A probes `B:5910`. B's Mode 1 listener completes the handshake whether or not C or the target is alive.
- A therefore reports `online` for a dead target. `ECONNREFUSED` only ever means "B's daemon isn't listening".
- Worse, every probe is a real `accept()` on B, which then dials C, which dials the target. A 15–30s probe interval per target becomes a connect/drop storm at every tier, hitting VNC servers with half-open sessions.
- Fix: probe direct targets with connect plus a read of the 12-byte RFB banner (the server speaks first). For chained targets, use the status API and never touch the data-plane port.

**C2. Port-positional routing has no target-identity binding, so a silent misroute is possible.**
- A's `5910` means "whatever B currently maps at 5910".
- If B's operator edits, re-offsets, or reorders ports, A's viewers land on a different machine with no error. In OT, connecting to the wrong CNC is a safety incident.
- Bind each chained device to a stable downstream `device_id`. Have the status poll return `{device_id → virtual_port}` and raise "chain drift" on mismatch.

**C3. Cycle detection is unsound in three ways.**
- **It is import-time only.** Hand-edited YAML and concurrent `import --apply --all` runs from deployment pipelines bypass it. Two relays importing each other simultaneously both pass. No runtime guard exists, and a data-plane loop is a self-amplifying socket and goroutine bomb.
- **The IP check contradicts the spec's own rationale.** Section 4.3 says overlapping RFC1918 addresses across branches are the reason `relay_id` exists, then §4.2 blocks on "`relay_id` *or IP*" in hops. That causes false positives on `192.168.1.10`-style duplicates. Non-adjacent hop IPs (e.g. `192.168.10.15` as seen from the Hub) are meaningless outside their own subnet. Use `relay_id` only.
- **It is relay-level, not port-level.** A→B on port 5910 plus B→A on a different port to a direct target is a legitimate bidirectional topology, and it gets rejected.

**C4. `relay_id` uniqueness is not guaranteed.**
- Pi fleets are mass-imaged, so cloned SD cards share `/etc/machine-id`. The default hostname `raspberrypi` collides too.
- Alpine containers have no machine-id, and the container hostname changes on recreate. The ID is not "persistent".
- `machine-id(5)` says the raw value is confidential and should be hashed with an app-specific key. Exposing it on an unauthenticated endpoint violates that.
- Fix: write-once random ID at first start, stored in a tiny state file. Alternatively use `HMAC(machine-id, "microvnc-relay")` plus a clone-detection check on import.

**C5. `POST /chain/inspect` has an auth contradiction, SSRF, and a trust-boundary gap.**
- The spec describes zero credentials, but inspect calls the downstream's `/api/v1/devices`, which the API table marks **Session**-authenticated. Either the downstream exposes devices unauthenticated, or the wizard transports downstream admin credentials through the upstream, over plain HTTP.
- It fetches an operator-supplied URL from a box that straddles network segments. That is a pivot: loopback services, cloud metadata, OT-subnet port scanning via error and timing oracles, DNS rebinding, and redirect following.
- The response is untrusted but gets written into `targets.yaml`. Risks:
  - unbounded JSON causing OOM on a 256MB device;
  - YAML injection and stored XSS via `name` and `hops`;
  - forged `relay_id` values (including the importer's own) to cause false loop blocks or fake breadcrumbs.
- `migrate-ip` rewrites hosts by a self-asserted `relay_id` with no verification that the new IP answers with the same ID.

**C6. `POST /auth/setup` is a public first-run takeover.**
- First request to the web port wins. Wiping the Docker volume, or deleting `auth.json` to reset a password, silently reopens it.
- There is no mention of a mutex or `O_EXCL` for concurrent setup calls.
- Require a one-time setup code printed to the journal or stdout, or restrict setup to loopback.

**C7. The `Headless Pure Relay` profile cannot participate in a chain.**
- `/api/v1/*` is active only when `webui_enabled: true`, yet the ~5–6MB headless profile is the headline Pi 1/Zero deployment. The tier you most want diagnosable is the one that can't serve `/chain/status`.
- A `web_bind` of loopback, which is the safe default, also makes it unreachable.
- Add a separate `features.chain_api_enabled` and `chain.bind` with a minimal listener: no sessions, no assets, cached data only.

**C8. The error taxonomy doesn't match what Go and Linux actually return.**
- **Pinger timeouts are not `ETIMEDOUT`.** A Go dial with a deadline returns an `os.ErrDeadlineExceeded` "i/o timeout". Kernel `ETIMEDOUT` appears only after ~127s of SYN retries. Classify with `net.Error.Timeout()`.
- **An unplugged host on the same L2 returns `EHOSTUNREACH`**, not a timeout, after ARP failure (~3s). The "unplugged → timeout" mapping is wrong for exactly the Floor Pi case.
- **Missing classes:**
  - `ENETUNREACH` (WireGuard or `wt0` down)
  - `EHOSTUNREACH`
  - `EPERM` and `ENOKEY` (WireGuard AllowedIPs mismatch)
  - `*net.DNSError`
  - local `EMFILE` and `ENOBUFS`, which blame the target for the prober's own exhaustion
  - an `unknown` or `unsupported` state for downstreams lacking the new endpoint (version skew in mixed Pi fleets)
- **The refused banner is wrong.** "daemon not running" is shown for any RST, but RST can also come from a stopped VNC service or a wrong port.

**C9. Health propagation has staleness, recursion, and fan-in problems.**
- Each hop re-reports a cached probe from the hop below, and probes run every 15–30s. Hop N's view lags by up to N×30s plus API latency, so breadcrumbs mix snapshots from different times and flap.
- "Bubble up multi-hop breadcrumbs" implies B's response embeds C's. If B's handler aggregates recursively, a cycle becomes infinite HTTP recursion across your fleet.
- The pinger is per-target, so N targets behind one downstream means N status polls. Poll per `(host, web_port)` once.
- `chain/status` must never trigger live probes. That would be an unauthenticated amplification vector.

**C10. Metering likely defeats `splice(2)` [verify in code].**
- Go's `io.Copy` reaches splice only when both ends are bare `*net.TCPConn`. Any wrapper such as `MeteredConn` falls back to the 32KB userspace copy.
- `bandwidth_metering_enabled` is on by default and the WebUI shows live bandwidth, so the default path likely never splices.
- If so, "zero-copy" is true only for the opt-out config. Implement `ReadFrom` on the wrapper that delegates to the underlying `TCPConn` and counts the returned `n`. Token-bucket QoS inherently forces the copy path.

**C11. Atomic 64-bit counters on 32-bit ARM [verify in code].**
- `atomic.AddUint64` on a misaligned struct field panics on `GOARCH=arm`. Use `atomic.Uint64` (auto-aligned), and put a `GOARCH=arm go vet` and 32-bit test in CI.

**C12. Single-core bcrypt is a CPU DoS.**
- At cost 10 on a 700MHz ARMv6, one bcrypt compare is roughly a second of CPU. The limiter only caps failures per IP.
- Concurrent logins from 256+ addresses, or one IPv6 /64, stall the data plane on a single core.
- The 256-IP bound means eviction under flood drops lockout entries, which bypasses the limiter.
- "Progressive delay" implemented as handler `sleep` holds goroutines and sockets, a slowloris against yourself.
- Fixes:
  - check the lockout before bcrypt
  - a semaphore of 1 on bcrypt
  - key IPv6 by /64
  - evict the least-offending unlocked entry first
  - reject immediately instead of sleeping

---

## 3. Overlooked Edge Cases & Operational Risks

**Memory and kernel resources**
- **RSS is the wrong yardstick for the budget.** Splice pipe buffers (64KB per pipe) and TCP autotuned socket buffers (multi-MB on high-BDP paths) are kernel memory that RSS excludes. The 10-stream test (likely loopback or LAN) doesn't exercise it. Measure RSS plus `Slab` plus socket memory under netem loss and latency.
- **`GOMEMLIMIT=48MiB` exceeds the stated 35MB ceiling.** The limit is soft, so set it to ~24–28MiB.
- **`MemoryMax=128M` may be inert.** The memory cgroup is often disabled by default on Raspberry Pi OS (`cgroup_disable=memory`). Verify `memory.max` is actually enforced, and add `MemoryHigh`.
- **The "Dashboard Only" row is inconsistent.** It shows higher load RSS than Full Stack, and the ranges are estimates under a heading that says "empirically verified". Pi 1/Zero (ARMv6) has no measurements at all, only Pi 2.
- **Swap defeats "zero flash wear".** The default `dphys-swapfile` lives on the SD card. Also check whether journald persists the daemon's stdout.
- **File descriptors:** a spliced stream costs ~6 fds per relay (2 sockets plus pipe pairs). Intermediate relays hold both legs. Verify `LimitNOFILE` and the per-port connection cap.
- **Probe TIME_WAIT:** probes accumulate TIME_WAIT on the prober's side, so use `SO_LINGER 0` for probe sockets.

**Network behavior**
- **MTU and PMTUD:**
  - A SYN probe never exercises large segments, so a PMTU blackhole (NetBird's lower MTU, nested overlays, ICMP-dropping firewalls) reads `online` while real sessions stall on the first big framebuffer update.
  - Each hop terminates TCP, so MSS is per-hop, which helps.
  - Recommend `tcp_mtu_probing=1` on relay hosts, and a probe that actually reads data.
- **Split-TCP bufferbloat:** per-hop send buffers plus 64KB pipes stack up. Cap `SO_SNDBUF` and set `TCP_NOTSENT_LOWAT`. The "3+ hops" advisory should use measured per-hop RTT summed via status (`rtt_ms` in the response), not a hop count.
- **Dead-peer detection:**
  - Go's default 15s keepalive doesn't fire while unacked data is in flight. Set `TCP_USER_TIMEOUT` (~30s).
  - Without it, WireGuard peers that lose power leave goroutines, fds, and `sessions` entries forever.
  - Half-close handling must `CloseWrite` and bound the wait on the other direction.
  - NAT and conntrack idle timeouts need the keepalive too.
- **DNS:**
  - A `CGO_ENABLED=0` binary uses Go's pure resolver. It has no `.local` mDNS, so `raspberrypi.local` hosts fail.
  - There's no caching, so each dial hits DNS.
  - A boot-time failure becomes an unclassified error.
  - NetBird DNS names would remove most of the IP-migration pain, but they require a `dns_error` class, last-good caching, and re-resolution on dial.
- **Middleboxes:**
  - Firewalls that forge RSTs, SYN proxies that answer for dead hosts, and tarpits all yield false `refused` or `online` readings.
  - Add hysteresis: N consecutive failures before a state flip.
  - Backlog overflow on a saturated Pi Zero looks like a powered-off device.
- **Probe starvation:** 4 workers × 3s timeout × 100 dead targets is ~75s per cycle against a 15–30s interval. Guard against overlapping cycles and prioritize.
- **Boot ordering:** `After=network-online.target` without `Wants=` doesn't pull the target in. Binding overlay IPs before `wt0` exists gives `EADDRNOTAVAIL`. Use per-listener retry with backoff (or `IP_FREEBIND`) rather than crashing and hammering restarts.

**Pi-specific correctness**
- **No RTC:** wall-clock jumps at NTP sync on boot. A telemetry ring indexed by wall-clock minute, plus lockout and session timers, will corrupt or misfire. Use monotonic time, and challenge-response instead of timestamps for any signed requests.
- **Power-cut persistence:** "atomic write" needs fsync of the temp file, rename, then fsync of the directory, with a `.bak`.
  - The daemon user also needs write access to the whole config directory for the rename, which undermines the "read-only config.yaml" claim.
  - Concurrent SIGHUP reload, batch API writes, and multiple WebUI sessions need a mutex and ETag-style optimistic concurrency.

**Mode 2 (UltraVNC repeater)**
- Reading the 250-byte ID needs a short deadline (~5s). Without it, trickling connections pin goroutines and fds in the pre-registration phase, which the 64-slot cap does not cover.
- Idle-but-alive sockets pass the `MSG_PEEK` liveness check, so 64 junk registrations lock out legitimate servers.
- An ID is a guessable bearer secret. First-registered wins unless duplicate-ID behavior is defined, so squatting an ID lets an attacker impersonate a server and capture the DES challenge-response.
- Viewer-before-server ordering and the pending-viewer queue are unspecified, and the queue needs a bound and timeout.
- Bind `:5500` to the overlay interface only, and handle the TOCTOU between the alive check and the splice start.

**Mode 3 (WebSocket bridge)**
- **CSWSH:** session-cookie auth on `/ws/vnc/{id}` allows cross-site WebSocket hijacking by any page the admin visits. Enforce `Origin`, `SameSite=Strict`, and CSRF protection on state-changing REST endpoints.
- **Memory:** disable `permessage-deflate` (hundreds of KB of zlib state per connection on a slow CPU, for already-compressed pixels). Set a read limit, write deadlines, ping/pong, and a global WS client cap, including for the 1Hz telemetry socket.
- **Scope risk:** the claim of a "minimal zero-dependency" HTML5 RFB client is the roadmap's riskiest item. Tight and ZRLE decode need inflate and JPEG handling. Vendor upstream noVNC (MPL-2.0/BSD, so check licensing) instead of hand-rolling it.

**Network exposure model**
- **Segmentation bypass:** Mode 1 is an unauthenticated TCP forwarder. Per-device bind interface and source ACLs aren't specified; `access_control` appears only in Mode 3's checklist. On a multi-homed Pi bridging OT and DMZ, this is a segmentation bypass by default.
- **Mesh-wide read access:** `allowed_subnets: 100.64.0.0/10` lets every NetBird peer read your topology (internal IPs, `origin_host`, relay IDs). That is a mesh-wide information disclosure, not "zero credential".
- **Docker source IPs:** bridge NAT shows the gateway IP, so the subnet ACL and per-IP limiter both degrade. Also check IPv4-mapped IPv6 normalization in the CIDR checks.
- **Plaintext last hops:** mid-chain LAN hops (`192.168.10.15`) are cleartext RFB with weak DES auth. State this in the trust model.

---

## 4. Actionable Improvements (before Phase 2)

1. **Normalize the data model.**
   - Replace per-device `hops[]` copies with a `relays:` table (`relay_id → host, web_port, name`) and `via: [relay_id, …]` on each device. A mid-hop IP change then becomes a one-record edit, and `migrate-ip` shrinks to a single-record update.
   - It also fixes the denormalization and stale-snapshot problem, and removes unreachable non-adjacent IPs from the model.
   - Add `schema_version` to `targets.yaml`.

2. **Redesign the status API as an adjacency report.**
   - Each relay reports only its own state: `{relay_id, version, capabilities, devices:[{device_id, virtual_port, state, last_error_class, age_s, rtt_ms}], downstream:[relay_id…]}`.
   - The upstream computes breadcrumbs client-side by BFS with a visited set and a depth and fan-out cap. This detects live cycles, avoids server-side recursion, and keeps handlers cache-only.
   - Support `If-None-Match`, a response size cap, and one poll per downstream relay.

3. **Define a real error taxonomy.**
   - States: `ok`, `refused`, `timeout`, `host_unreachable`, `net_unreachable`, `dns_error`, `local_error`, `unsupported`, `unknown`.
   - Include `last_change` timestamps and hysteresis.
   - Map kernel and Go errors explicitly, including ARP-failure `EHOSTUNREACH`.
   - Have the data plane record per-target dial errors, so a real viewer failure is classified by the relay that actually dialed.

4. **Add a runtime loop guard.**
   - Refuse an inbound connection on port P if its source address equals the host that P is configured to dial (catches A→B→A).
   - Add per-port and global connection caps as a backstop for longer cycles.
   - Treat import-time `relay_id`-only checks as UX, not safety.

5. **Fix identity.**
   - Use a write-once random ID in a state file, or `HMAC(machine-id, app)`.
   - Verify via status poll on import and on `migrate-ip` that the target answers with the expected ID and `device_id`s.
   - Refuse to import an ID that equals your own.

6. **Rework the chain trust model.**
   - Add `chain.shared_secret` in `config.yaml` with HMAC-SHA256 on status and inspect requests. Use a server-issued nonce, since Pis lack a reliable clock. It's cheap on ARMv6 and avoids bcrypt, per-IP exposure to the whole mesh, and admin-credential relaying.
   - Add an opt-in `export: true` per device and a purpose-built `GET /chain/export` returning minimal fields, instead of reusing the session-authenticated `/devices`.

7. **Harden inspect.**
   - Restrict it to a static `chain.allowed_downstreams` allowlist.
   - Allow only `http` or `https` schemes, and pin the resolved IP.
   - Disable redirects.
   - Apply `io.LimitReader` and device-count caps, strict schema decoding, and short timeouts.
   - Never reflect raw upstream errors.
   - Re-validate every imported field server-side, and render all names via `textContent`.

8. **In-band diagnostics for viewers.**
   - For ports tagged `protocol: rfb`, emit a spec-compliant RFB failure (version banner, zero security types, reason string) naming the failed hop. This puts the diagnosis in front of the person who needs it.

9. **Mode 2 and Mode 3 hardening:** apply the deadlines, caps, duplicate-ID policy, deflate-off, read limits, and Origin checks listed in §3.

10. **Close the setup hole.** Use a one-time setup code and a mutex or `O_EXCL`. Also add:
    - session idle and absolute expiry
    - cookie flags
    - a separate `chain.bind`

11. **Verification infrastructure moves earlier.**
    - CI (currently Phase 4) should start with Phase 2, because that phase adds the first complex network-facing code. Include `govulncheck`, `-race` on amd64 or arm64 (the race detector does not support 32-bit ARM), and a 32-bit ARM build and test for atomic alignment.
    - Build a netns plus `tc netem` multi-hop harness covering blackhole, RST, loss, MTU, and flap scenarios.
    - Add `goleak`, fuzzing of the YAML and import parsers, and a 72-hour soak with 200+ streams that asserts goroutine count, kernel memory, and fd count.
    - Measure on a real Pi 1 or Zero before claiming those tiers.

12. **Re-sequence Phase 2.**
    - **2a:** error taxonomy, correct probe design, persistent relay ID, schema versioning, chain API listener, and tests. It ships on its own.
    - **2b:** status adjacency API, loop guard, and drift detection.
    - **2c:** import (inspect, batch, CLI, wizard) once the trust model from items 6 and 7 is settled. Currently the wizard UX is scheduled before the auth model it depends on exists.

13. **Packaging corrections for Phase 4.**
    - Debian `armhf` means ARMv7+, so a Pi 1 or Zero needs a distinct GOARM=6 artifact and `.deb` architecture label, or `SIGILL` follows.
    - Docker on a 256MB device adds ~50–80MB for dockerd and containerd. Document that cost, and require `--network host` or an explicit source-IP strategy so ACLs and the limiter work.
