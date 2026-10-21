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
# Duration is in seconds. The CSV and a matching log are written next to the
# script invocation. Provenance (git commit, Go version, stress binary hash)
# is recorded in both the console log and the report.
set -euo pipefail

ORIG_ARGS=("$@")

RELAY="127.0.0.1"
WEB_PORT="8080"
MODE1_PORT="5910"
STREAMS="10"
DURATION="86400"
STAGE_SECONDS="30"
RATE_MB="0.25"
BYTES_PER_STREAM="1048576"
SSH_HOST=""
RSS_LIMIT_KB="32768"
HEARTBEAT_MS_LIMIT="500"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --relay) RELAY="$2"; shift 2 ;;
        --web-port) WEB_PORT="$2"; shift 2 ;;
        --mode1-port) MODE1_PORT="$2"; shift 2 ;;
        --streams) STREAMS="$2"; shift 2 ;;
        --duration) DURATION="$2"; shift 2 ;;
        --stage-seconds) STAGE_SECONDS="$2"; shift 2 ;;
        --rate-mb) RATE_MB="$2"; shift 2 ;;
        --bytes-per-stream) BYTES_PER_STREAM="$2"; shift 2 ;;
        --ssh-host) SSH_HOST="$2"; shift 2 ;;
        --rss-limit-kb) RSS_LIMIT_KB="$2"; shift 2 ;;
        --heartbeat-ms) HEARTBEAT_MS_LIMIT="$2"; shift 2 ;;
        -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
        *) echo "Unknown argument: $1" >&2; exit 2 ;;
    esac
done

if ! command -v go >/dev/null 2>&1; then
    echo "Error: Go is required to build cmd/stress-tester" >&2
    exit 1
fi

STAMP="$(date +%Y%m%d-%H%M%S)"
STRESS_BIN="$(mktemp -t microvnc-stress.XXXXXX)"
CSV="soak-${STAMP}.csv"
LOG="soak-${STAMP}.log"
cleanup() {
    rm -f "$STRESS_BIN"
}
trap cleanup EXIT

# Mirror everything to the log file so the artifact carries its own provenance.
exec > >(tee -a "$LOG") 2>&1

echo "==> Building stress-tester..."
go build -o "$STRESS_BIN" ./cmd/stress-tester

STRESS_SHA="$(sha256sum "$STRESS_BIN" | awk '{print $1}')"
GIT_COMMIT="$(git rev-parse HEAD 2>/dev/null || echo unknown)"
GO_VERSION="$(go version)"
echo "==> Soak: relay=$RELAY duration=${DURATION}s streams=$STREAMS stage=${STAGE_SECONDS}s rate=${RATE_MB}MB/s per stream bytes_per_stream=${BYTES_PER_STREAM} csv=$CSV log=$LOG"
echo "==> Command: scripts/soak.sh ${ORIG_ARGS[*]:-}"
echo "==> Provenance: git=${GIT_COMMIT} go=${GO_VERSION} stress_sha256=${STRESS_SHA}"
if [[ -n "$SSH_HOST" ]]; then
    echo "==> Remote metrics via ssh $SSH_HOST"
fi

echo "timestamp,event,rss_kb,cgroup_bytes,slab_kb,free_mb,fd_count,heartbeat_ok,heartbeat_ms,stage_ok,stage_err,tx_bytes" > "$CSV"

END=$((SECONDS + DURATION))
MISSED=0
SLOW=0
STAGE=0
ABORT=0

record() {
    local event="$1"
    local stage_ok="${2:-}" stage_err="${3:-}" tx_bytes="${4:-}"
    local rss="n/a" cgroup="n/a" slab="n/a" free_mb="n/a" fds="n/a"
    local heartbeat="0" heartbeat_ms="n/a"
    local heartbeat_time=""

    if heartbeat_time="$(curl -sf -m 5 -o /dev/null -w '%{time_total}' \
        "http://${RELAY}:${WEB_PORT}/api/v1/auth/status" 2>/dev/null)"; then
        heartbeat="1"
        heartbeat_ms="$(awk -v t="$heartbeat_time" 'BEGIN { printf "%.0f", t * 1000 }')"
        MISSED=0
        if (( heartbeat_ms > HEARTBEAT_MS_LIMIT )); then
            SLOW=$((SLOW + 1))
        else
            SLOW=0
        fi
    else
        MISSED=$((MISSED + 1))
        SLOW=0
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

    echo "$(date -Is),$event,$rss,$cgroup,$slab,$free_mb,$fds,$heartbeat,$heartbeat_ms,$stage_ok,$stage_err,$tx_bytes" >> "$CSV"
    echo "[${event}] rss=${rss}KB hb_ok=${heartbeat} hb_ms=${heartbeat_ms} ok=${stage_ok:-n/a} err=${stage_err:-n/a} tx=${tx_bytes:-n/a}"

    if [[ "$heartbeat" == "0" && "$MISSED" -ge 2 ]]; then
        echo "CIRCUIT BREAKER: two consecutive failed HTTP checks; aborting." >&2
        ABORT=1
        return
    fi
    if (( SLOW >= 2 )); then
        echo "CIRCUIT BREAKER: two consecutive heartbeats above ${HEARTBEAT_MS_LIMIT}ms; aborting." >&2
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
    echo "==> Stage $STAGE: $STREAMS streams for ${STAGE_SECONDS}s (rate=${RATE_MB}MB/s per stream, bytes_per_stream=${BYTES_PER_STREAM})"
    STAGE_START=$SECONDS
    STAGE_OUT="$("$STRESS_BIN" \
        -target-relay "$RELAY" \
        -web-port "$WEB_PORT" \
        -mode1-port "$MODE1_PORT" \
        -mode concurrency \
        -concurrency "$STREAMS" \
        -duration "${STAGE_SECONDS}s" \
        -rate-mb "$RATE_MB" \
        -bytes-per-stream "$BYTES_PER_STREAM" \
        -watchdog=true 2>&1 || true)"
    echo "$STAGE_OUT"

    # The stress tester exits as soon as it has sent bytes_per_stream. Hold the
    # stage open to the configured cadence so a 24h run produces ~2,880 stages
    # and does not hammer the board with back-to-back ramps.
    STAGE_ELAPSED=$((SECONDS - STAGE_START))
    if (( STAGE_ELAPSED < STAGE_SECONDS )); then
        sleep $((STAGE_SECONDS - STAGE_ELAPSED))
    fi

    stage_ok="$(sed -n 's/.*RESULT ok=\([0-9][0-9]*\).*/\1/p' <<<"$STAGE_OUT" | tail -n1)"
    stage_err="$(sed -n 's/.*RESULT .*err=\([0-9][0-9]*\).*/\1/p' <<<"$STAGE_OUT" | tail -n1)"
    tx_bytes="$(sed -n 's/.*tx_bytes=\([0-9][0-9]*\).*/\1/p' <<<"$STAGE_OUT" | tail -n1)"

    record "stage-$STAGE" "${stage_ok:-0}" "${stage_err:-0}" "${tx_bytes:-0}"
    (( ABORT == 1 )) && break
    # No fixed inter-stage sleep: each stress stage already waits for teardown
    # and the next stage should start immediately (matches the historical
    # ~29.6 s/stage cadence recorded in the 24h soak artifact).
done

record "end"

# Baseline check: file descriptors (and therefore leaked sockets) must return
# to roughly the starting level. Remote metrics are n/a without --ssh-host.
START_FDS="$(awk -F, 'NR==2 {print $7}' "$CSV")"
END_FDS="$(awk -F, 'END {print $7}' "$CSV")"
if [[ "$START_FDS" =~ ^[0-9]+$ && "$END_FDS" =~ ^[0-9]+$ ]]; then
    if (( END_FDS > START_FDS + 2 )); then
        echo "BASELINE FAILURE: FDs grew from ${START_FDS} to ${END_FDS}." >&2
        ABORT=1
    else
        echo "==> Baseline OK: FDs ${START_FDS} -> ${END_FDS}"
    fi
fi

echo "==> Soak finished. CSV: $CSV log: $LOG"
echo "==> Thresholds: RSS <= ${RSS_LIMIT_KB}KB, available RAM >= 400MB, heartbeat <= ${HEARTBEAT_MS_LIMIT}ms, no two consecutive failures/slow beats, end FDs <= start + 2."
if (( ABORT == 1 )); then
    exit 1
fi
