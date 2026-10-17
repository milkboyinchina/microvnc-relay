report by chatgpt.com think mode

# μVNC Relay — Principal Architecture Audit

Review date: 17 October 2026 · Scope: `BLUEPRINT.md`, the published repository, and relevant Linux/Go networking behavior

## Executive verdict

The architecture is viable as a lightweight TCP relay, but the proposed Phase 2 cascade design is not yet safe enough to implement exactly as written. The biggest risks are not the byte-forwarding concept itself; they are incomplete trust boundaries, inconsistent identity and routing semantics, misleading network diagnostics, and insufficiently bounded control-plane operations.

The blueprint specifies an immediate-next-hop data plane, ordered hop metadata, a lightweight downstream health API, remote inspection, and batch IP migration. These are sensible building blocks, but the specification leaves critical behavior undefined around identity authentication, concurrent configuration changes, cycle detection under stale metadata, and failure attribution across multiple hops.&#x20;

BLUEPRINT.md



One important distinction: the public repository's changelog labels the project Alpha (`v0.1.0-alpha`), describes the existing Mode 1/Mode 2 relay and telemetry foundations, and places cascaded relay features in the future roadmap. The Phase 2 endpoints should therefore be treated as design proposals, not as verified, production-ready implementations.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://github.com\&sz=32)

GitHub

+1



## 1. Architectural scorecard

Overall architecture readiness

# 40 / 100

Phase 2: No-go as written

Equal weighting across five dimensions. This is my engineering readiness assessment, not a benchmark result.

Architecture, Protocol & Multi-Hop Design

### 5/10

The routing model is promising, but graph invariants and identity trust are incomplete.

Edge Resource Efficiency (<35MB & Zero-Cache)

### 6/10

The forwarding approach is efficient, but concurrency and RSS bounds are not hard guarantees.

Diagnostic & Failure Isolation Precision

### 3/10

The proposed error mapping overstates what TCP connect errors establish.

Security, Auth & SSRF Protection

### 2/10

Management-plane exposure, bootstrap authentication and remote inspection need redesign.

Phasing, Roadmap & Completeness

### 4/10

The broad order is sensible, but implementation gaps and missing security prerequisites block Phase 2.

### The most consequential finding: the specification and repository do not match

This audit distinguishes documented design intent from behavior visible in the repository. The repository is identified as Alpha, and the changelog describes the current relay foundations while listing important cascaded-relay capabilities as planned work.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://github.com\&sz=32)

GitHub

+1



Several supposedly completed Phase 1 properties are also absent or materially different in the public source:

| Blueprint claim                                         | Repository observation                                                                                                                         | Severity |
| ------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------- | -------- |
| Separate `config.yaml` and `targets.yaml`               | The current configuration model holds device definitions in the same `config.yaml` model.                                                      | High     |
| Mode 2 pending-server cap of 64 and dead-socket pruning | The inspected Mode 2 code has a pending map but no documented capacity limit or `MSG_PEEK` eviction implementation.                            | Critical |
| Completed bounded four-worker probe scheduler           | The inspected pinger processes device probes serially, while other entry points can start additional probes.                                   | High     |
| Completed login brute-force limiter                     | The visible login route has no limiter wired into it.                                                                                          | Critical |
| `--reload` CLI flag and coherent live reload            | The entry point does not implement the documented flag; the SIGHUP reload path does not replace configuration across all dependent components. | High     |

\|  The Mode 2, pinger, and authentication discrepancies appear in their respective source paths.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://raw.githubusercontent.com\&sz=32)

raw\.githubusercontent.com

+5



This is not cosmetic documentation drift. It means some of the constraints the design assumes are already enforced are not currently enforced by the inspected implementation.

## 2. Critical flaws & architectural vulnerabilities

### 2.1 Cascaded routing is conceptually correct, but the graph model is underspecified

Verdict: keep immediate-next-hop routing; redesign the schema and graph validator before implementing import.

The blueprint's central separation is the right direction:

- The active route directs a local virtual port to the immediate downstream relay.
- Origin information records the physical destination.
- `chain.hops` retains the path for diagnostics and visualization.

This prevents an upstream relay from trying to connect directly to a private address that exists only inside a downstream subnet.&#x20;

BLUEPRINT.md



However, a few semantic questions are left unanswered.

First, the current data model overloads `host` and `port`. Those fields mean a physical device address for a direct target and the next relay's virtual address for a chained target. Every consumer must reliably interpret `type` before deciding whether those fields identify a physical endpoint or a relay route. The current repository's `DeviceConfig` does not yet contain the proposed `type` and chain metadata fields.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://raw.githubusercontent.com\&sz=32)

raw\.githubusercontent.com

+1



Second, `chain.hops` has no formal invariant. The specification doesn't precisely define whether the list includes the current relay, only downstream relays, or the terminal physical target. It also doesn't define how the list changes when an intermediate relay is replaced, renamed, or moved to another overlay address.

That ambiguity will produce stale breadcrumbs, incorrectly detected cycles, or metadata that disagrees with the active forwarding path.

Third, the loop check is insufficient for a production control plane. Checking the importing relay's `relay_id` or IP against a supplied downstream hop list is useful, but it only proves something about the graph snapshot that was inspected. It does not protect against concurrent imports, an outdated chain list, a relay identity collision, manual configuration edits, or a migration that introduces a cycle.

Consider:

`A:5910 → B:5910 → C:5910 → A:5910`

Each TCP connection may be locally valid. Yet an initial client connection can recursively trigger forwarding between the relays, creating additional connections until resources are exhausted. IP TTL does not solve this: these are separate TCP connections, not one IP packet circulating forever.

The phrase “uncapped data-plane hop depth” is therefore too permissive. A relay does not need expensive per-byte hop processing, but its control plane must prove that the configured route graph is acyclic before activating it.

Required fix: store graph edges explicitly and validate the complete proposed graph before commit, on startup, on reload, and on API edits. Fail closed when an identity or route cannot be resolved. Set a configurable maximum hop depth to protect management operations and diagnostic complexity, even though forwarding itself remains a transparent TCP relay.

### 2.2 `relay_id` is an identifier, not proof of identity

The blueprint proposes a persistent identity derived from `/etc/machine-id` or the hostname.&#x20;

BLUEPRINT.md



Neither is sufficient as the trust anchor for an IP migration workflow:

- Hostnames can be duplicated or changed.
- Machine IDs can be copied into cloned images or container filesystems.
- A restored filesystem can duplicate an identity that was previously unique.
- An identity returned by an unauthenticated HTTP endpoint can be spoofed.

The proposed “Same ID, New IP” migration assumes that an observed `relay_id` identifies the same trusted node. Without authentication of the node, it only proves that the remote endpoint claimed the same ID.

Required fix: generate a random persistent UUID for node identity, pair it with a persistent public/private key or another authenticated mesh identity, and verify that identity before changing routes. Treat hostname and overlay IP as mutable display/routing attributes, not identity authorities. Detect duplicate IDs and require explicit operator resolution.

### 2.3 Zero-credential health polling is not automatically safe

The blueprint proposes a read-only `/api/v1/chain/status` endpoint guarded by `access_control.allowed_subnets`. It would disclose relay identity, port health, and failure breadcrumbs without an administrator password.&#x20;

BLUEPRINT.md



Avoiding downstream administrator passwords is good. But “zero credentials” does not mean “zero authentication risk.”

The disclosed health data can reveal:

- Relay identities and internal topology.
- Which virtual ports exist.
- Which sites or devices are unreachable.
- Potentially sensitive operational patterns in an industrial network.

A CIDR allowlist is also a coarse trust boundary. If all of `100.64.0.0/10` is considered trusted, any compromised peer able to reach the endpoint from that range may be able to enumerate the mesh's relay health. Access should follow actual mesh peer policy and role, not merely membership in a broad address range.

There is an immediate implementation concern: the inspected HTTP route setup does not apply the documented `access_control.allowed_subnets` protection as a universal HTTP middleware. The current API is primarily gated by session authentication, while the IP check appears in proxy connection handlers. Simply adding the new endpoint to the router will not inherit the required protection.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://raw.githubusercontent.com\&sz=32)

raw\.githubusercontent.com

+2



A further issue is that the WebUI's proposed inspector queries `/api/v1/devices`, while the API contract currently marks that route as session-authenticated. The blueprint doesn't define how the upstream inspector authenticates to it.&#x20;

Required fix:

1. Publish a dedicated, read-only relay-export API rather than exposing the full device management API.
2. Authenticate the querying relay through a peer identity, client certificate, or another narrowly scoped mechanism.
3. Authorize exactly which peer can inspect which relay and which fields it may receive.
4. Return only local identity and locally cached health—not admin sessions, credentials, or unrestricted downstream metadata.
5. Include an API schema version, the observation timestamp, and the age of each health result.

If the UI is disabled for headless deployments, the control API's availability must be defined separately. In the current lifecycle, disabling the WebUI also disables the HTTP server; a chain status endpoint cannot work if its parent server never starts.&#x20;

### 2.4 `/api/v1/chain/inspect` presents a real SSRF boundary

Verdict: do not implement arbitrary downstream URLs.

The proposed upstream backend accepts a downstream URL, requests data from it, and returns a preview to the operator. That is a classic server-side request forgery (SSRF) boundary: the relay is being asked to make HTTP requests on another party's behalf.

A malicious or compromised administrator account could potentially make the inspector request services beyond the intended relay, depending on how the URL and redirects are handled. Risks include loopback services, host-local management endpoints, link-local endpoints, internal APIs, and DNS rebinding.

This is not fixed by requiring a URL beginning with `http://` or `https://`. The actual connection destination must be validated, including the resolved IP address, and redirects must not escape the approved destination. OWASP's SSRF guidance specifically emphasizes robust destination validation and safe outbound request behavior.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://cheatsheetseries.owasp.org\&sz=32)

OWASP Cheat Sheet Series



A safe inspector should:

- Accept a registered relay ID or a pre-enrolled relay endpoint rather than an arbitrary URL.
- Permit only explicitly configured schemes, hosts, and ports.
- Resolve and validate every destination IP, including IPv4 and IPv6, and pin the outbound connection to the validated address.
- Reject loopback, link-local, metadata-service, and other prohibited destinations unless a narrowly documented exception applies.
- Disable redirects and unexpected proxy-environment behavior.
- Apply connection, total-operation, response-size, and JSON-decoding limits.
- Never forward the browser's session cookie or credentials to the downstream host.
- Authenticate the downstream relay independently of its IP address.
- Expose a dedicated read-only export endpoint instead of embedding downstream administrator credentials in `targets.yaml`.

The final point is important: the upstream should not require the downstream administrator's reusable password merely to inspect port metadata. Use a scoped machine credential with revocation and rotation, or an authenticated peer identity.

### 2.5 The authentication model has serious bootstrap risks

The current configuration defaults include a nonempty bcrypt `PasswordHash` annotated as the default `"admin"`, while `AuthEnabled` defaults to true. The auth manager can fall back to the configured credentials when `auth.json` is absent. As a result, the first-run setup state can be bypassed when the configuration already supplies a hash. Depending on that hash's validity and the code path, this risks either a known default credential or an authentication lockout.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://raw.githubusercontent.com\&sz=32)

raw\.githubusercontent.com

+1



The blueprint states that first-run setup should create administrator credentials. The implementation should not simultaneously preload a usable default hash.

There is also a setup race in the visible authentication implementation: the handler checks `SetupRequired()` and then calls the credential-setting function separately. Two concurrent first-run requests may both pass the check before either commits the change.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://raw.githubusercontent.com\&sz=32)

raw\.githubusercontent.com

+1



A further detail: the session cookie is set `HttpOnly` and `SameSite=Lax`, but it is not marked `Secure`. With the UI bound on all interfaces and TLS explicitly delegated to an overlay or reverse proxy, a reachable plaintext HTTP path could expose session cookies.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://raw.githubusercontent.com\&sz=32)

GitHub

+2



Required fix: use a truly empty first-run state, make bootstrap credential creation an atomic one-time operation, and require loopback/console access or a one-time bootstrap secret. Set `Secure` on cookies when HTTPS is in use, bind the management plane to the trusted overlay interface where possible, and explicitly reject unencrypted remote management outside a documented trusted deployment configuration.

### 2.6 The brute-force limiter is described as completed but is not wired into login

The public API's login handler calls credential verification and creates a session without the proposed `LoginRateLimiter` being attached to that route.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://raw.githubusercontent.com\&sz=32)

GitHub

+2



This discrepancy is more serious than the choice between five and ten retries. A limiter that exists in a design document or isolated struct but is not invoked at the public endpoint supplies no protection.

Even after wiring it in, per-IP-only limits need care on a mesh or NAT-heavy deployment. One hostile client might rotate source addresses; many legitimate clients might share one source IP; and an in-memory limiter resets on restart. A fixed 256-IP map also needs a defined saturation policy, rather than evicting active lockouts in an attacker-controlled way.

Required fix: enforce the limiter in the login handler or middleware; bound request bodies; include progressive delay and a bounded eviction strategy; record limited events in the RAM ring buffer; and test it through the actual HTTP endpoint.

### 2.7 Configuration updates are not reliably transactional

The blueprint describes atomic writes and an atomic batch import. But on the inspected implementation path, `AddOrUpdateDevice` can mutate the in-memory configuration and only then validate the result. If validation fails, the invalid mutation has already occurred. Separately, some API handlers log config save failures and still respond as if the operation succeeded, while live-reload errors are ignored.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://raw.githubusercontent.com\&sz=32)

GitHub

+3



This creates a dangerous control-plane/data-plane divergence:

- The WebUI may report success.
- The in-memory config may contain invalid or unpersisted state.
- The on-disk configuration may contain the previous state.
- The listeners may continue using an older configuration.

For batch cascade imports, that can leave some routes activated while other routes are not persisted, or cause later edits to overwrite unexpected state.

Required fix: build a candidate configuration snapshot; validate the entire candidate including graph constraints and port collisions; write the correct configuration file to a temporary path; atomically commit; then apply listeners and dependent subsystems. If apply fails, roll back or return an explicit partial-failure state with a safe recovery procedure. Never return HTTP success after a failed persistence or activation step.

Also resolve the dual-file model deliberately. Separate daemon settings and targets are reasonable, but define ownership, independent validation, backup/recovery, permissions, revisions, and which operation may modify each file.

## 3. Diagnostic precision: TCP errors are not root-cause labels

The blueprint equates:

- `ECONNREFUSED` → host is online but the service is stopped.
- `ETIMEDOUT` → host is powered off, unplugged, or firewalled.

Those are overconfident conclusions.

The operating system's `connect(2)` documentation describes `ECONNREFUSED` as a connection attempt receiving a refusal, typically because no listener exists at the destination. A timeout means the connection attempt did not complete within the relevant time. Neither errno proves the physical state of a remote machine.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://www.man7.org\&sz=32)

Linux manual page

+1



### What the error actually tells you

| Observation                                       | Defensible conclusion                                                              | What it does not prove                                                               |
| ------------------------------------------------- | ---------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------ |
| `ECONNREFUSED` / TCP RST                          | Something actively rejected this connection attempt.                               | That the intended physical host is online, or that the VNC process alone is stopped. |
| `ETIMEDOUT`                                       | The connection handshake did not complete before the timeout.                      | That the host is powered off or the cable is unplugged.                              |
| `ENETUNREACH` / `EHOSTUNREACH`                    | The local routing stack or a network intermediary reported a reachability failure. | The exact physical point of failure.                                                 |
| DNS lookup failure                                | The name could not be resolved successfully by the selected resolver.              | That the downstream host is offline.                                                 |
| Local descriptor exhaustion                       | The relay could not allocate a local socket/descriptor.                            | That the target is unreachable.                                                      |
| HTTP/API timeout while the relay's TCP port works | Management polling did not complete.                                               | That the relay's forwarding service is down.                                         |

A firewall can be configured to reject with a TCP reset instead of silently dropping traffic. NAT gateways, proxies, and policy devices can also reject connections on behalf of a destination. Conversely, packet loss, a blackholing firewall, asymmetric routing, overlay state, overloaded networking, or MTU/path issues can produce timeouts even when the remote host is powered on.

The current implementation has another limitation: the inspected `DeviceProbeStatus` provides a simple online state and last-error string rather than the typed multi-state model proposed by the blueprint. Its probe uses a normal TCP dial operation, which performs a connection attempt—not merely an isolated raw SYN measurement. The `ProbeAll` loop processes targets serially, contrary to the documented bounded four-worker model.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://raw.githubusercontent.com\&sz=32)

GitHub

+2



### Multi-hop attribution requires more than forwarding an error string

Suppose the hub cannot query the Floor Pi. There are at least three materially different possibilities:

1. The hub cannot reach the Site DMZ relay.
2. The Site DMZ relay is reachable, but its management API is unavailable or blocked.
3. The Site DMZ relay works, but its cached report says the Floor Pi is unhealthy.

These states must not all become “target offline.”

Each probe record should contain at minimum:

`observer_relay_id`, `target_relay_id` or `device_id`, `probe_kind`, `status`, `error_class`, `elapsed_ms`, `observed_at`, `last_success_at`, and `stale_after`.

The user-facing status should be derived from the observation. Examples include refused, connect timeout, routing failure, DNS failure, local resource failure, management unavailable, and stale observation. Add a separate “likely cause” or operator hint, but do not present it as established fact.

The downstream status response should report locally cached, timestamped observations rather than performing recursive downstream fan-out whenever an upstream polls it. Otherwise, one status request can amplify into many network operations, producing resource spikes exactly when the network is degraded.

Diagnostic score: 3/10. The desired observability is good, but the proposed one-to-one errno mapping is unsafe for operations in segmented networks.

## 4. Edge resource viability:

Linux `splice(2)` can reduce userspace copying for eligible TCP-to-TCP paths. In Go, `io.Copy` can use optimized `WriterTo`/`ReaderFrom` implementations, while its general fallback uses a 32KiB temporary buffer. The use of pooled buffers for metering and rate-limited paths is reasonable, but it needs to be measured in the actual forwarding call graph.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://go.dev\&sz=32)

Go.dev

+1



### 4.1 `sync.Pool` is not a memory cap

The 32KiB-per-direction pool is not a fixed-size allocation budget. For metered forwarding, two simultaneous directions can require approximately 64KiB of userspace copy buffers per active stream before accounting for goroutine stacks, maps, socket state, kernel socket buffers, and WebUI activity.

Illustrative raw buffer requirements if every stream occupies both directions:

| Concurrent streams | Buffer memory alone |
| ------------------ | ------------------- |
| 10                 | \~0.625MiB          |
| 100                | \~6.25MiB           |
| 500                | \~31.25MiB          |

These are arithmetic estimates, not measured RSS. They assume both 32KiB buffers are held per stream and exclude all overhead. The point is that unconstrained concurrency can consume the 35MB budget even when the pool behaves exactly as intended.

`sync.Pool` is an allocation-reuse mechanism, not an admission-control mechanism. The daemon needs hard limits for active streams, accepted sockets, pending matchmaker connections, queued work, and probes.

### 4.2 `GOMEMLIMIT=48MiB` does not enforce

Use three distinct measures:

- RSS / peak RSS: process resident footprint.
- Go runtime memory: `runtime.MemStats` and runtime-managed memory.
- Cgroup memory: total memory charged to the service, with kernel-related costs depending on the accounting environment.

Do not use one to claim the other.

A hard deployment limit must combine bounded user-space allocations, session limits and socket-buffer tuning with measurements of RSS and cgroup memory. Keep headroom: if the entire 35MB budget is available only in ideal steady-state operation, bursts in handshakes, GC, logging, and reconnection will cause surprises.

### 4.3 There are two immediate resource-bound discrepancies

Mode 2's advertised pending-server cap is not present in the inspected code. The current implementation has a pending-server map, but the proposed maximum of 64 and dead-socket eviction cannot be verified in that implementation.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://raw.githubusercontent.com\&sz=32)

GitHub

+1



Probe concurrency is also not globally bounded. The main probe loop is serial, but device create/update and test paths can launch separate probe goroutines. An operator, hostile authenticated client, or retry storm can therefore create more concurrent network activity than the nominal scheduler suggests.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://raw.githubusercontent.com\&sz=32)

GitHub

+2



### 4.4 Shutdown can hang on active or half-open sessions

The forwarding path waits for both directions to finish, and the inspected proxy managers wait for their tracked goroutines during shutdown. However, the managers do not appear to maintain a registry that reliably closes every already-matched active client/server socket during `Stop`. With idle timeout disabled by default, a surviving stream can block shutdown indefinitely.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://raw.githubusercontent.com\&sz=32)

GitHub

+2



This is a lifecycle defect, not merely a memory optimization. It can delay configuration changes and service restarts, and it complicates the management of leaked or half-open connections.

Required fix: maintain an authoritative active-session registry, stop admitting new sessions during shutdown, close all active sockets, wait only up to a fixed grace period, and report sessions still pending termination. Configure read, write, and connection-lifecycle bounds; a read deadline alone does not bound a blocked write.

### 4.5 ARMv6 remains a separate validation target

The reported stress measurement is on a Raspberry Pi 2, not evidence for a 700MHz ARMv6 device. The changelog's visible cross-compilation notes include ARMv7, ARM64, and AMD64, but do not establish that `GOARM=6` is continuously built and tested.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://github.com\&sz=32)

GitHub



Even if ARMv6 compiles, it may have a materially different CPU and throughput envelope under metering, TLS-proxied management, GC, or high connection churn. Phase 2 should not be gated by compile success alone: test it on physical ARMv6 hardware, using representative RFB traffic and slow-network conditions.

Resource score: 6/10. The streaming model is sound, but a specific ten-stream RSS result is not a concurrency bound or a guarantee for ARMv6.

## 5. Overlooked edge cases & operational risks

| Area                    | Failure mode                                                                                                                | Required operational control                                                                                                          |
| ----------------------- | --------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| WireGuard / NetBird MTU | Handshakes succeed but sustained traffic stalls or retransmits due to MTU/PMTUD problems or path-specific black holes.      | Test large bidirectional streams and different MTUs; collect retransmit and zero-window indicators; verify ICMP/PMTUD behavior.       |
| Socket exhaustion       | Too many accepted sockets, concurrent handshakes, or probe connections exhaust file descriptors or memory.                  | Global/per-source/session caps, admission control, overload status, and FD exhaustion diagnostics.                                    |
| Mode 2 slowloris        | A 5-second handshake deadline bounds one connection's handshake, but does not limit how many handshakes run concurrently.   | Bound pre-auth sockets and handshakes; add per-source limits and total pending capacity.                                              |
| Stale reverse IDs       | A disconnected matchmaker server remains registered and can be reported as available.                                       | Pending-server TTL, close-event tracking, and liveness state distinct from the existence of a map entry.                              |
| Duplicate Mode 2 IDs    | A new endpoint using an existing ID can replace a pending endpoint.                                                         | Treat the ID as a selector, not a credential; authenticate or constrain peers and surface takeover events.                            |
| DNS                     | Temporary name-resolution failures or changing A/AAAA records are misreported as target timeouts.                           | Retain structured DNS errors; define refresh and fallback policy; record the resolved endpoint used by a probe.                       |
| Health API              | Relay TCP port is reachable but the downstream HTTP API is disabled or filtered.                                            | Separate forwarding-port state, API reachability, and downstream target health.                                                       |
| Probe amplification     | Many upstream relays or UI requests trigger simultaneous downstream checks.                                                 | Cache downstream results, jitter polls, deduplicate identical in-flight probes, and impose global/per-peer concurrency caps.          |
| HTTP body handling      | Header timeout exists, but slow request bodies and oversized JSON may still consume connections or memory.                  | Apply request body limits and body-read deadlines; validate batch counts and field lengths.                                           |
| Mode 3 WebSocket bridge | Frame processing, masking, control frames, write queues, and backpressure create allocations that pure TCP splice does not. | Bound frames, queued bytes, sessions and write duration; enforce ping/pong timeout, Origin policy and per-target authorization.       |
| No flash wear           | System logs are forwarded to journald, which may persist logs depending on the host's journal configuration.                | Make the no-persistent-log configuration explicit; distinguish no stream caching from intentionally persisted config and credentials. |

The Mode 2 concerns are particularly important because the blueprint describes `maxPendingServers=64` and nonblocking socket pruning as already completed, yet those features are absent from the code reviewed here.&#x20;

[image](https://www.google.com/s2/favicons?domain=https://raw.githubusercontent.com\&sz=32)

GitHub

+2



### The “zero flash wear” requirement needs a more precise contract

The application-level policy—no SQLite/WAL, no persistent traffic cache, bounded RAM log and telemetry buffers—is coherent. It does not, by itself, ensure that the whole appliance performs zero flash writes.

The daemon's systemd configuration sends standard output and error to the journal. Whether those logs reach persistent storage depends on the host's journald configuration.&#x20;

Also, configuration and credentials intentionally persist to disk. Thus the defensible requirement is:

> No VNC/RFB payload caching and no periodic telemetry/logging writes by the application; persistent writes limited to explicitly requested configuration and credential changes, with host-level logs configured according to the deployment's durability policy.

This preserves the real objective without promising behavior controlled by another daemon.

## 6. Actionable improvements before Phase 2

### Priority 0 — Close current implementation gaps

These are release blockers, not optional cleanup.

1\. Reconcile the blueprint with the current source. Decide whether separate config and target files, the login limiter, Mode 2 pending cap, bounded probes, and the reload command are genuinely implemented. Update the blueprint or the code before extending the design.

2\. Remove default administrator credentials from first-run configuration. Ensure an empty credential state cannot be skipped, make bootstrap creation atomic, and wire the rate limiter into the real login route.

3\. Add hard connection and resource limits. At minimum, cap total active relay sessions, connections per target, accepted but unauthenticated connections, Mode 2 pending servers, simultaneous handshakes, and active probes. Define a deterministic behavior when a limit is reached.

4\. Fix shutdown and config commit semantics. Track and close active sockets; use candidate configuration snapshots; validate before mutation; surface save and apply failures; and never claim successful commits that were not persisted and activated.

5\. Add actual ARMv6 verification to continuous integration. Cross-compile `GOARM=6` and `GOARM=7` separately, and periodically test a physical Pi 1/Pi Zero and Pi 2. Treat the existing memory report as a baseline test case, not a substitute for a defined stress envelope.

### Priority 1 — Define the chain contract before coding routes

Make the route representation explicit. For example, define three independent concepts:

```
id: cnc-lathe-2
name: CNC Lathe #2
enabled: true

route:
  kind: relay
  relay_id: site-dmz-gw
  endpoint:
    host: 100.64.0.2
    port: 5910

origin:
  asset_id: cnc-lathe-2
  host: 10.0.1.50
  port: 5900

chain:
  # Diagnostic snapshot only; not used to forward traffic.
  hops:
    - relay_id: site-dmz-gw
    - relay_id: floor-pi-line1
```

This is illustrative, not a drop-in schema. Its intent is to stop callers silently treating a terminal origin as an active next hop.

Then define and test invariants:

- Every relay has one stable identity and a verified identity key.
- A route refers to exactly one immediate downstream endpoint.
- The complete active graph is acyclic.
- Metadata can be stale without silently changing actual routing.
- A chain import cannot commit if its downstream snapshot is incomplete or ambiguous.
- Port conflicts and graph constraints are checked in the same transaction.
- Revisions prevent stale UI previews from overwriting newer configuration.

### Priority 2 — Build typed, bounded diagnostics

Use explicit status types rather than making the UI infer root causes from strings. Each result needs an observer, target, timestamp, duration, error class, last successful check, and staleness policy.

Apply a shared probe scheduler with bounded workers, per-host in-flight deduplication, a global concurrency limit, jittered polling, short connection deadlines, and cancellation. A user-triggered probe should go through the same scheduler rather than bypassing resource controls.

For cascades, the upstream should query only the immediate downstream relay. That relay returns its current cached observations with timestamps. Do not make a status request trigger recursive probing of the entire subtree.

### Priority 3 — Secure and bound the inspection API

Before implementing `POST /api/v1/chain/inspect`:

- Enroll trusted relay endpoints and authenticate the downstream identity.
- Expose a scoped read-only export rather than using the administrator-facing devices endpoint.
- Reject untrusted destinations, redirects, unsafe DNS resolutions, and excessive responses.
- Add strict HTTP request-body and operation timeouts.
- Decide whether the chain API works in headless mode and implement that behavior deliberately.
- Log inspection and migration actions to the fixed-size RAM ring buffer, including failures, without logging secrets.

### Priority 4 — Make IP migration a verified transaction

A safe migration is not “find every target matching a relay ID and replace the host field.”

It should be:

1. Discover a candidate endpoint.
2. Authenticate that endpoint and prove it controls the expected relay identity.
3. Preview exactly which active route endpoints will change.
4. Check reachability, port conflicts, graph cycles, and current configuration revision.
5. Apply the update as one validated transaction.
6. Verify management and route health.
7. Roll back or report an explicit failed state if activation is unsuccessful.

Only rewrite the immediate routing endpoint for affected edges. Do not rewrite the terminal asset's `origin_host` merely because a relay's overlay IP changed.

## 7. Recommended implementation roadmap

The four existing phases are ordered sensibly at a broad level: get the core relay working, add cascades, add browser visualization, then improve packaging. But Phase 2 combines too many high-risk concerns and should be split into independently testable gates.

Gate 0

### Stabilize Phase 1

Correct auth bootstrap/rate limiting, Mode 2 admission bounds, probe concurrency, shutdown, config transactions, reload behavior, and ARMv6 build/test coverage.

Exit condition: no unbounded control paths; reliable persistence and activation; tested failure recovery.

Gate 1

### Phase 2A — Identity and graph schema

Implement route/origin/metadata separation, stable authenticated relay identity, schema versioning, and whole-graph cycle/depth validation.

Exit condition: tests for A→B→A, deeper cycles, stale metadata, duplicate IDs, colliding private subnets, and concurrent edits.

Gate 2

### Phase 2B — Diagnostics and status API

Add typed errors, cached health responses, staleness, authenticated peer policy, a bounded scheduler, and headless-compatible endpoint behavior.

Exit condition: no misleading “host offline” claims from raw errno and no request-triggered recursive probe storms.

Gate 3

### Phase 2C — Inspect, import and migration

First implement a read-only, SSRF-resistant inspector; then dry-run/preview; then atomic batch commit and authenticated IP migration.

Exit condition: authenticated discovery, revision checks, accurate error reporting, and tested rollback.

Phase 3

### Topology and noVNC

Build the topology view from validated graph state. Add WebSocket bridging only after session limits, auth, frame limits, backpressure, and graceful teardown are tested.

Phase 4

### Packaging and fleet distribution

Alpine multi-architecture images, native packages, install scripts and release publication should follow the stable contracts—but build/test/release CI needs to start now, not in Phase 4.

### Required acceptance tests before production use

Do not declare Phase 2 ready until the following are repeatable:

- Graph correctness: direct target, one-hop chain, 3+ hops, self-loop, multi-node loop, duplicated relay identity, stale chain metadata, and concurrent import/migration.
- Security: bootstrap race, login throttling through HTTP, unauthorized health polling, forged relay identity, unsafe inspect URL, redirects, DNS rebinding, oversized JSON, and session-cookie handling.
- Failure isolation: RST rejection, silent packet drop, downstream management API disabled, DNS failure, routing error, local file-descriptor exhaustion, and stale status reporting.
- Resource containment: maximum connections, maximum pending Mode 2 IDs, concurrent handshakes, slow readers/writers, abrupt peer disconnects, and service shutdown during live streams.
- Soak testing: long-running representative streams on Pi 2 and ARMv6 hardware, with `/proc` peak RSS, cgroup memory, open FDs, goroutine count, and connection count recorded.
- Data-plane verification: demonstrate when Linux `splice(2)` is actually used and measure the allocations and throughput of metered, rate-limited, and future WebSocket paths independently.

## Final recommendation

Do not start Phase 2 by implementing the import wizard or topology visualization. Start by correcting the mismatches between the specification and the existing source. Then formalize the chain schema and identity trust model, build graph validation and bounded health reporting, and only then expose batch migration and inspection to the WebUI.

The immediate-next-hop design is worth retaining. The three design decisions I would reject as written are uncapped chain depth without strict graph enforcement, host-state conclusions derived directly from `ECONNREFUSED`/`ETIMEDOUT`, and an inspection API whose destination and downstream trust are not rigorously constrained. They are all fixable, but leaving them unresolved would turn otherwise useful functionality into a routing reliability and security liability.

&#x20;Would a monthly repo/spec regression audit help catch implementation drift before releases?
