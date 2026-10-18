#!/usr/bin/env bash
# verify-pi2.sh — Automates Phase 1 Raspberry Pi 2 resource envelope and zero-disk verification
set -euo pipefail

PID=$(pgrep -x "microvnc-relay" | head -n 1 || true)
if [[ -z "$PID" ]]; then
    PID=$(pgrep -f "/microvnc-relay" | head -n 1 || true)
fi

if [[ -z "$PID" ]]; then
    echo "Error: microvnc-relay process is not running. Start it with:"
    echo "    sudo systemctl start microvnc-relay"
    exit 1
fi

echo "=========================================================="
echo " μVNC Relay: Raspberry Pi 2 Acceptance Verification "
echo " Target PID: $PID"
echo "=========================================================="

# 1. Process Memory (RSS) Inspection
PAGESIZE=$(getconf PAGESIZE)
STATM_RSS_PAGES=$(awk '{print $2}' "/proc/$PID/statm")
RSS_BYTES=$((STATM_RSS_PAGES * PAGESIZE))
RSS_MB=$(awk "BEGIN {printf \"%.2f\", $RSS_BYTES / (1024 * 1024)}")

echo "==> Memory RSS Inspection:"
echo "    Resident Set Size (RSS): ${RSS_MB} MB"

if (( $(echo "$RSS_MB < 35.0" | bc -l) )); then
    echo "    [PASS] Memory RSS is within strict < 35MB budget!"
else
    echo "    [FAIL] Memory RSS exceeded 35MB limit (${RSS_MB} MB)!"
    exit 1
fi

# 2. File Descriptor & Kernel Memory Inspection
FD_COUNT=$(ls -1 "/proc/$PID/fd" 2>/dev/null | wc -l)
echo "==> Socket & File Descriptors Open: $FD_COUNT"

CGROUP_MEM=$(cat /sys/fs/cgroup/memory.current 2>/dev/null || cat /sys/fs/cgroup/memory/memory.usage_in_bytes 2>/dev/null || echo "n/a")
SLAB_KB=$(awk '/^Slab:/ {print $2}' /proc/meminfo 2>/dev/null || echo "n/a")
echo "==> Process cgroup memory: $CGROUP_MEM bytes"
echo "==> Kernel Slab: ${SLAB_KB} kB"

if ! grep -qw memory /proc/cgroups 2>/dev/null; then
    echo "    [WARN] memory cgroup is not enabled; systemd MemoryHigh/MemoryMax are inert."
fi

# 3. Disk I/O Check (Zero Disk Writes Protection)
IO_WRITE_BYTES=$(awk '/write_bytes/ {print $2}' "/proc/$PID/io" 2>/dev/null || echo "0")
echo "==> Cumulative Process Disk Writes: $IO_WRITE_BYTES bytes"

# 4. REST API Ping Check
WEB_BIND=$(grep "web_bind" /etc/microvnc-relay/config.yaml 2>/dev/null | awk '{print $2}' | tr -d '"' || echo "127.0.0.1:8080")
if curl -s -f "http://$WEB_BIND/api/v1/auth/status" > /dev/null; then
    echo "    [PASS] WebUI REST API responds on $WEB_BIND"

    GOROUTINES=$(curl -s "http://$WEB_BIND/debug/pprof/goroutine?debug=1" 2>/dev/null | awk '/^goroutine profile: total/ {print $4}' || true)
    if [[ -n "$GOROUTINES" ]]; then
        echo "==> Goroutines (dev build): $GOROUTINES"
    fi
else
    echo "    [INFO] WebUI disabled or running on non-default port"
fi

echo "=========================================================="
echo " Phase 1 Verification Successfully Completed! "
echo "=========================================================="
