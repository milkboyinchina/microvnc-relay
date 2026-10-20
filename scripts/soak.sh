#!/usr/bin/env bash
# soak.sh — Drive repeated 30s stream ramps against a relay and record metrics.
#
# Run this from a workstation, never from the Pi itself. The stress generator
# runs locally; only the relay daemon runs on the target board.
#
# Usage:
#   scripts/soak.sh --relay 100.100.131.74 --duration 86400 --streams 10 \
#                   --ssh-host nb-api-apipi
#
# Duration is in seconds. A CSV is written next to the script invocation.
set -euo pipefail

RELAY="127.0.0.1"
WEB_PORT="8080"
MODE1_PORT="5910"
STREAMS="10"
DURATION="86400"
SSH_HOST=""
RSS_LIMIT_KB="32768"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --relay) RELAY="$2"; shift 2 ;;
        --web-port) WEB_PORT="$2"; shift 2 ;;
        --mode1-port) MODE1_PORT="$2"; shift 2 ;;
        --streams) STREAMS="$2"; shift 2 ;;
        --duration) DURATION="$2"; shift 2 ;;
        --ssh-host) SSH_HOST="$2"; shift 2 ;;
        --rss-limit-kb) RSS_LIMIT_KB="$2"; shift 2 ;;
        -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
        *) echo "Unknown argument: $1" >&2; exit 2 ;;
    esac
done

if ! command -v go >/dev/null 2>&1; then
    echo "Error: Go is required to build cmd/stress-tester" >&2
    exit 1
fi

STRESS_BIN="$(mktemp -t microvnc-stress.XXXXXX)"
CSV="soak-$(date +%Y%m%d-%H%M%S).csv"
cleanup() {
    rm -f "$STRESS_BIN"
}
trap cleanup EXIT

echo "==> Building stress-tester..."
go build -o "$STRESS_BIN" ./cmd/stress-tester

echo "timestamp,event,rss_kb,cgroup_bytes,slab_kb,free_mb,fd_count,heartbeat_ok" > "$CSV"
echo "==> Soak: relay=$RELAY duration=${DURATION}s streams=$STREAMS csv=$CSV"
if [[ -n "$SSH_HOST" ]]; then
    echo "==> Remote metrics via ssh $SSH_HOST"
fi

END=$((SECONDS + DURATION))
MISSED=0
STAGE=0
ABORT=0

record() {
    local event="$1"
    local rss="n/a" cgroup="n/a" slab="n/a" free_mb="n/a" fds="n/a"
    local heartbeat="0"

    if curl -sf -m 2 "http://${RELAY}:${WEB_PORT}/api/v1/auth/status" >/dev/null 2>&1; then
        heartbeat="1"
        MISSED=0
    else
        MISSED=$((MISSED + 1))
    fi

    if [[ -n "$SSH_HOST" ]]; then
        read -r rss cgroup slab free_mb fds < <(
            ssh -o BatchMode=yes -o ConnectTimeout=3 "$SSH_HOST" '
                PID=$(pgrep -x microvnc-relay | head -n1 || true)
                if [ -z "$PID" ]; then echo "n/a n/a n/a n/a n/a"; exit 0; fi
                RSS=$(awk "/^VmRSS:/ {print \$2}" /proc/$PID/status 2>/dev/null || echo n/a)
                CG=$(cat /sys/fs/cgroup/memory.current 2>/dev/null || cat /sys/fs/cgroup/memory/memory.usage_in_bytes 2>/dev/null || echo n/a)
                SLAB=$(awk "/^Slab:/ {print \$2}" /proc/meminfo)
                FREE=$(awk "/^MemAvailable:/ {print int(\$2/1024)}" /proc/meminfo)
                FDS=$(ls /proc/$PID/fd 2>/dev/null | wc -l)
                echo "$RSS $CG $SLAB $FREE $FDS"
            ' 2>/dev/null || echo "n/a n/a n/a n/a n/a"
        )
    fi

    echo "$(date -Is),$event,$rss,$cgroup,$slab,$free_mb,$fds,$heartbeat" >> "$CSV"

    if [[ "$heartbeat" == "0" && "$MISSED" -ge 2 ]]; then
        echo "CIRCUIT BREAKER: two missed heartbeats; aborting." >&2
        ABORT=1
        return
    fi
    if [[ "$rss" =~ ^[0-9]+$ ]] && (( rss > RSS_LIMIT_KB )); then
        echo "CIRCUIT BREAKER: RSS ${rss}KB exceeds ${RSS_LIMIT_KB}KB; aborting." >&2
        ABORT=1
        return
    fi
    if [[ "$free_mb" =~ ^[0-9]+$ ]] && (( free_mb < 400 )); then
        echo "CIRCUIT BREAKER: available RAM ${free_mb}MB < 400MB; aborting." >&2
        ABORT=1
        return
    fi
}

record "start"

while (( SECONDS < END )); do
    STAGE=$((STAGE + 1))
    echo "==> Stage $STAGE: $STREAMS streams for 30s"
    "$STRESS_BIN" \
        -target-relay "$RELAY" \
        -web-port "$WEB_PORT" \
        -mode1-port "$MODE1_PORT" \
        -mode concurrency \
        -concurrency "$STREAMS" \
        -duration 30s \
        -watchdog=true || true

    record "stage-$STAGE"
    (( ABORT == 1 )) && break
    sleep 10
done

record "end"
echo "==> Soak finished. CSV: $CSV"
echo "==> Thresholds: RSS <= ${RSS_LIMIT_KB}KB, available RAM >= 400MB, no two missed heartbeats."
