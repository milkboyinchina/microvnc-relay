#!/usr/bin/env bash
# gate0-compose.sh — Gate 0 dev-only Docker data-plane harness.
#
# Scenarios:
#   - direct Hub -> target
#   - chained Hub -> Site DMZ -> Floor Pi -> target
#   - abrupt viewer disconnect and session drain
#   - injected RST and drop faults at the Site DMZ
#   - optional tc netem (GATE0_WITH_NETEM=1)
#   - optional A -> B -> A loop (GATE0_WITH_LOOP=1)
#
# Control-plane identity/drift/version-skew scenarios land with Phase 2a/b/c.
set -euo pipefail

COMPOSE_FILE="deploy/dev/compose.yaml"
HUB_API="http://127.0.0.1:18080"
DMZ_API="http://127.0.0.1:18081"

# Keep bind-mounted config writes owned by the host user.
export HOST_UID="$(id -u)"
export HOST_GID="$(id -g)"

HUB_TARGETS_BACKUP=""
DMZ_TARGETS_BACKUP=""
if [[ "${GATE0_WITH_LOOP:-0}" == "1" ]]; then
    HUB_TARGETS_BACKUP="$(mktemp)"
    DMZ_TARGETS_BACKUP="$(mktemp)"
    cp deploy/dev/hub/targets.yaml "$HUB_TARGETS_BACKUP"
    cp deploy/dev/site-dmz/targets.yaml "$DMZ_TARGETS_BACKUP"
fi

cleanup() {
    docker compose -f "$COMPOSE_FILE" down -v --remove-orphans >/dev/null 2>&1 || true
    if [[ -n "$HUB_TARGETS_BACKUP" && -f "$HUB_TARGETS_BACKUP" ]]; then
        cp "$HUB_TARGETS_BACKUP" deploy/dev/hub/targets.yaml 2>/dev/null || \
            echo "WARN: could not restore deploy/dev/hub/targets.yaml" >&2
        chmod 0644 deploy/dev/hub/targets.yaml 2>/dev/null || true
        rm -f "$HUB_TARGETS_BACKUP"
    fi
    if [[ -n "$DMZ_TARGETS_BACKUP" && -f "$DMZ_TARGETS_BACKUP" ]]; then
        cp "$DMZ_TARGETS_BACKUP" deploy/dev/site-dmz/targets.yaml 2>/dev/null || \
            echo "WARN: could not restore deploy/dev/site-dmz/targets.yaml" >&2
        chmod 0644 deploy/dev/site-dmz/targets.yaml 2>/dev/null || true
        rm -f "$DMZ_TARGETS_BACKUP"
    fi
}
trap cleanup EXIT

wait_for_api() {
    local url="$1" name="$2"
    for _ in $(seq 1 60); do
        if curl -sf -m 2 "$url/api/v1/auth/status" >/dev/null 2>&1; then
            echo "==> $name API ready"
            return 0
        fi
        sleep 1
    done
    echo "Error: $name API did not become ready" >&2
    docker compose -f "$COMPOSE_FILE" logs --tail=50 || true
    return 1
}

echo "==> Building and starting the Gate 0 dev harness..."
docker compose -f "$COMPOSE_FILE" up -d --build

wait_for_api "$HUB_API" "Hub"
wait_for_api "$DMZ_API" "Site DMZ"

echo "==> Scenario: direct Hub -> target"
python3 - 15910 <<'PY'
import socket, sys
port = int(sys.argv[1])
s = socket.create_connection(("127.0.0.1", port), 5)
s.settimeout(5)
banner = s.recv(12)
assert banner.startswith(b"RFB"), banner
s.sendall(b"PING")
data = b""
while len(data) < 4:
    chunk = s.recv(4 - len(data))
    if not chunk:
        break
    data += chunk
assert data == b"PING", data
s.close()
print("OK direct")
PY

echo "==> Scenario: Hub -> Site DMZ -> Floor Pi -> target"
python3 - 15911 <<'PY'
import socket, sys
port = int(sys.argv[1])
s = socket.create_connection(("127.0.0.1", port), 5)
s.settimeout(5)
banner = s.recv(12)
assert banner.startswith(b"RFB"), banner
s.sendall(b"PING")
data = b""
while len(data) < 4:
    chunk = s.recv(4 - len(data))
    if not chunk:
        break
    data += chunk
assert data == b"PING", data
s.close()
print("OK chained")
PY

echo "==> Scenario: abrupt disconnect drains sessions"
python3 - 15911 <<'PY'
import socket, sys
port = int(sys.argv[1])
s = socket.create_connection(("127.0.0.1", port), 5)
s.settimeout(5)
_ = s.recv(12)
s.close()
PY
for _ in $(seq 1 30); do
    count=$(curl -s "$HUB_API/api/v1/sessions" | python3 -c 'import json,sys; print(len(json.load(sys.stdin).get("sessions", [])))' 2>/dev/null || echo 1)
    [[ "$count" == "0" ]] && break
    sleep 0.5
done
if [[ "$count" != "0" ]]; then
    echo "Error: sessions did not drain after abrupt disconnect (count=$count)" >&2
    exit 1
fi
echo "OK sessions drained"

echo "==> Scenario: injected RST at Site DMZ"
curl -sf "$DMZ_API/debug/fault/rst?on=1" >/dev/null
python3 - 15911 <<'PY'
import socket, sys
port = int(sys.argv[1])
s = socket.create_connection(("127.0.0.1", port), 2)
s.settimeout(2)
try:
    data = s.recv(1)
except ConnectionResetError:
    print("OK rst (viewer saw ECONNRESET)")
except OSError:
    print("OK rst (viewer saw downstream failure)")
else:
    assert data == b"", data
    print("OK rst (viewer saw EOF after downstream RST)")
finally:
    s.close()
PY
# The injected fault is visible in the Site DMZ log even when the upstream
# relay translates it into a clean viewer-side EOF. This distinguishes RST from
# the drop fault below, which uses a different log line.
if ! docker compose -f "$COMPOSE_FILE" logs --since=2m site-dmz 2>/dev/null | grep -q "fault injection: resetting client"; then
    echo "Error: Site DMZ did not log the RST fault injection" >&2
    exit 1
fi
curl -sf "$DMZ_API/debug/fault/rst?on=0" >/dev/null

echo "==> Scenario: injected drop at Site DMZ"
curl -sf "$DMZ_API/debug/fault/drop?on=1" >/dev/null
python3 - 15911 <<'PY'
import socket, sys
port = int(sys.argv[1])
s = socket.create_connection(("127.0.0.1", port), 2)
s.settimeout(3)
try:
    data = s.recv(1)
except socket.timeout:
    raise AssertionError("expected graceful FIN/EOF for drop fault, got timeout")
finally:
    s.close()
assert data == b"", data
print("OK drop (FIN/EOF, distinct from RST)")
PY
if ! docker compose -f "$COMPOSE_FILE" logs --since=2m site-dmz 2>/dev/null | grep -q "fault injection: dropping client"; then
    echo "Error: Site DMZ did not log the drop fault injection" >&2
    exit 1
fi
curl -sf "$DMZ_API/debug/fault/drop?on=0" >/dev/null

if [[ "${GATE0_WITH_NETEM:-0}" == "1" ]]; then
    echo "==> Scenario: tc netem 50ms/1% on Site DMZ"
    if docker compose -f "$COMPOSE_FILE" exec -T -u 0 site-dmz \
        tc qdisc add dev eth0 root netem delay 50ms loss 1% 2>/dev/null; then
        python3 - 15911 <<'PY'
import socket, sys
port = int(sys.argv[1])
s = socket.create_connection(("127.0.0.1", port), 5)
s.settimeout(5)
banner = s.recv(12)
assert banner.startswith(b"RFB"), banner
s.sendall(b"PING")
data = b""
while len(data) < 4:
    chunk = s.recv(4 - len(data))
    if not chunk:
        break
    data += chunk
assert data == b"PING", data
s.close()
print("OK netem")
PY
        docker compose -f "$COMPOSE_FILE" exec -T -u 0 site-dmz tc qdisc del dev eth0 root || true
    else
        echo "INFO: tc netem unavailable in this environment; skipped"
    fi
fi

if [[ "${GATE0_WITH_LOOP:-0}" == "1" ]]; then
    echo "==> Scenario: A -> B -> A loop (admission caps must contain it)"
    curl -sf -X POST "$HUB_API/api/v1/devices" -H 'Content-Type: application/json' \
        -d '{"id":"loop-dmz","name":"Loop DMZ","host":"site-dmz","port":5912,"virtual_port":5912,"enabled":true}' >/dev/null
    curl -sf -X POST "$DMZ_API/api/v1/devices" -H 'Content-Type: application/json' \
        -d '{"id":"loop-hub","name":"Loop Hub","host":"hub","port":5912,"virtual_port":5912,"enabled":true}' >/dev/null

    python3 - 15912 <<'PY'
import socket, sys, time
port = int(sys.argv[1])
s = socket.create_connection(("127.0.0.1", port), 3)
s.settimeout(2)
end = time.time() + 2
while time.time() < end:
    try:
        if not s.recv(4096):
            break
    except OSError:
        break
s.close()
print("OK loop bounded")
PY

    loop_cid="$(docker compose -f "$COMPOSE_FILE" ps -q hub)"
    if [[ -n "$loop_cid" ]] && command -v docker >/dev/null 2>&1; then
        loop_stats="$(docker stats --no-stream --format '{{.CPUPerc}} {{.MemUsage}}' "$loop_cid" 2>/dev/null || true)"
        if [[ -n "$loop_stats" ]]; then
            python3 - "$loop_stats" <<'PY'
import re, sys
raw = sys.argv[1]
cpu = raw.split()[0].rstrip("%")
mem = raw.split()[1]
value = float(re.sub(r"[^0-9.]", "", mem))
if mem.endswith("GiB"):
    value *= 1024
elif mem.endswith("KiB"):
    value /= 1024
assert float(cpu) < 200.0, f"loop scenario CPU too high: {cpu}%"
assert value < 64.0, f"loop scenario RSS too high: {value:.1f} MiB"
print(f"OK loop resources bounded (cpu={cpu}% mem={value:.1f} MiB)")
PY
        fi
    fi

    curl -sf -X DELETE "$HUB_API/api/v1/devices/loop-dmz" >/dev/null || true
    curl -sf -X DELETE "$DMZ_API/api/v1/devices/loop-hub" >/dev/null || true

    # The hub must still answer after the loop is torn down.
    sleep 1
    curl -sf "$HUB_API/api/v1/auth/status" >/dev/null
    echo "OK loop teardown"
fi

echo "==> Gate 0 compose scenarios passed."
