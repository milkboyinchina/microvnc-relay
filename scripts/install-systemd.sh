#!/usr/bin/env bash
# install-systemd.sh — Installs microvnc-relay as a systemd service on Debian/Ubuntu/Raspberry Pi OS
set -euo pipefail

ARCH="$(uname -m)"
DEFAULT_BIN="./microvnc-relay-linux-armv7"
if [[ "$ARCH" == "armv6l" ]]; then
    DEFAULT_BIN="./microvnc-relay-linux-armhf"
elif [[ "$ARCH" == "aarch64" ]]; then
    DEFAULT_BIN="./microvnc-relay-linux-arm64"
elif [[ "$ARCH" == "x86_64" ]]; then
    DEFAULT_BIN="./microvnc-relay-linux-amd64"
fi

BIN_SRC="${1:-$DEFAULT_BIN}"
CONFIG_SRC="${2:-./config.example.yaml}"
TARGETS_SRC="${3:-./targets.example.yaml}"

echo "==> Installing μVNC Relay systemd service (detected arch: $ARCH)..."

if [[ $EUID -ne 0 ]]; then
   echo "Error: This script must be run as root (e.g. sudo bash scripts/install-systemd.sh)" >&2
   exit 1
fi

if [[ ! -f "$BIN_SRC" ]]; then
    # Try local native build if binary not passed
    if [[ -f "./microvnc-relay" ]]; then
        BIN_SRC="./microvnc-relay"
    else
        echo "Error: Binary not found at $BIN_SRC" >&2
        exit 1
    fi
fi

# 1. Install binary
echo "==> Copying binary ($BIN_SRC) to /usr/local/bin/microvnc-relay..."
install -m 0755 "$BIN_SRC" /usr/local/bin/microvnc-relay

# 2. Setup configuration directory and initial config + targets
mkdir -p /etc/microvnc-relay
# RuntimeDirectory is recreated by systemd on every boot; create it now for
# manual runs and for the PID file immediately after installation.
install -d -m 0750 /run/microvnc-relay
if [[ ! -f /etc/microvnc-relay/config.yaml ]]; then
    echo "==> Creating default /etc/microvnc-relay/config.yaml from $CONFIG_SRC..."
    cp "$CONFIG_SRC" /etc/microvnc-relay/config.yaml
    chmod 0640 /etc/microvnc-relay/config.yaml
else
    echo "==> Preserving existing /etc/microvnc-relay/config.yaml"
fi

if [[ ! -f /etc/microvnc-relay/targets.yaml && -f "$TARGETS_SRC" ]]; then
    echo "==> Creating default /etc/microvnc-relay/targets.yaml from $TARGETS_SRC..."
    cp "$TARGETS_SRC" /etc/microvnc-relay/targets.yaml
    chmod 0640 /etc/microvnc-relay/targets.yaml
elif [[ -f /etc/microvnc-relay/targets.yaml ]]; then
    echo "==> Preserving existing /etc/microvnc-relay/targets.yaml"
fi

# 3. Install MTU-probing sysctl. Keep existing unrelated settings intact.
SYSCTL_FILE="/etc/sysctl.d/99-microvnc-relay.conf"
if ! grep -qs '^net.ipv4.tcp_mtu_probing[[:space:]]*=' "$SYSCTL_FILE" 2>/dev/null; then
    echo "==> Enabling net.ipv4.tcp_mtu_probing=1..."
    {
        [[ -f "$SYSCTL_FILE" ]] && cat "$SYSCTL_FILE"
        echo "net.ipv4.tcp_mtu_probing = 1"
    } > "${SYSCTL_FILE}.tmp"
    chmod 0644 "${SYSCTL_FILE}.tmp"
    mv "${SYSCTL_FILE}.tmp" "$SYSCTL_FILE"
    sysctl --system >/dev/null 2>&1 || sysctl -p "$SYSCTL_FILE" >/dev/null 2>&1 || true
fi

# 4. Check that the memory cgroup is available; otherwise MemoryHigh/MemoryMax
# in the unit are inert.
if ! grep -qw memory /proc/cgroups 2>/dev/null; then
    echo "WARN: memory cgroup is not enabled; systemd MemoryHigh/MemoryMax will not enforce a ceiling." >&2
elif [[ ! -f /sys/fs/cgroup/memory.max && ! -f /sys/fs/cgroup/memory/memory.limit_in_bytes ]]; then
    echo "WARN: memory controller files not found; check cgroup v1/v2 configuration." >&2
fi

# 5. Install systemd service unit
echo "==> Installing systemd unit file..."
install -m 0644 packaging/systemd/microvnc-relay.service /etc/systemd/system/microvnc-relay.service

# 6. Reload and enable
systemctl daemon-reload
systemctl enable microvnc-relay.service

echo "==> Installation complete! To start μVNC Relay now, run:"
echo "    sudo systemctl start microvnc-relay"
echo "==> First-run setup token:"
echo "    sudo journalctl -u microvnc-relay -n 50 | grep -i 'setup token'"
echo "==> To inspect live logs:"
echo "    journalctl -u microvnc-relay -f"
